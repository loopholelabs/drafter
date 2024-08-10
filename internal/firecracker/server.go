package firecracker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/lithammer/shortuuid/v4"
	"github.com/loopholelabs/drafter/pkg/utils"
	"golang.org/x/sys/unix"
	"k8s.io/utils/inotify"
)

var (
	errSignalKilled = errors.New("signal: killed")

	ErrNoSocketCreated                = errors.New("no socket created")
	ErrFirecrackerExited              = errors.New("firecracker exited")
	ErrCouldNotCreateVMPathDirectory  = errors.New("could not create VM path directory")
	ErrCouldNotCreateInotifyWatcher   = errors.New("could not create inotify watcher")
	ErrCouldNotAddInotifyWatch        = errors.New("could not add inotify watch")
	ErrCouldNotReadNUMACPUList        = errors.New("could not read NUMA CPU list")
	ErrCouldNotStartFirecrackerServer = errors.New("could not start firecracker server")
	ErrCouldNotCloseWatcher           = errors.New("could not close watcher")
	ErrCouldNotCloseServer            = errors.New("could not close server")
	ErrCouldNotWaitForFirecracker     = errors.New("could not wait for firecracker")
)

const (
	FirecrackerSocketName = "firecracker.sock"
)

type FirecrackerServer struct {
	VMPath string
	VMPid  int
	Wait   func() error
	Close  func() error
}

func StartFirecrackerServer(
	ctx context.Context,

	firecrackerBin string,
	jailerBin string,

	chrootBaseDir string,

	uid int,
	gid int,

	netns string,
	numaNode int,
	cgroupVersion int,

	enableOutput bool,
	enableInput bool,
) (server *FirecrackerServer, errs error) {
	server = &FirecrackerServer{}

	goroutineManager := utils.NewGoroutineManager(
		ctx,
		&errs,
		utils.GoroutineManagerHooks{},
	)
	defer goroutineManager.WaitForForegroundGoroutines()
	defer goroutineManager.StopAllGoroutines()
	defer goroutineManager.CreateBackgroundPanicCollector()()

	id := shortuuid.New()

	server.VMPath = filepath.Join(chrootBaseDir, "firecracker", id, "root")
	if err := os.MkdirAll(server.VMPath, os.ModePerm); err != nil {
		panic(errors.Join(ErrCouldNotCreateVMPathDirectory, err))
	}

	watcher, err := inotify.NewWatcher()
	if err != nil {
		panic(errors.Join(ErrCouldNotCreateInotifyWatcher, err))
	}
	defer watcher.Close()

	if err := watcher.AddWatch(server.VMPath, inotify.InCreate); err != nil {
		panic(errors.Join(ErrCouldNotAddInotifyWatch, err))
	}

	cpus, err := os.ReadFile(filepath.Join("/sys", "devices", "system", "node", fmt.Sprintf("node%v", numaNode), "cpulist"))
	if err != nil {
		panic(errors.Join(ErrCouldNotReadNUMACPUList, err))
	}

	cmd := exec.CommandContext(
		ctx, // We use ctx, not internalCtx here since this resource outlives the function call
		jailerBin,
		"--chroot-base-dir",
		chrootBaseDir,
		"--uid",
		fmt.Sprintf("%v", uid),
		"--gid",
		fmt.Sprintf("%v", gid),
		"--netns",
		filepath.Join("/var", "run", "netns", netns),
		"--cgroup-version",
		fmt.Sprintf("%v", cgroupVersion),
		"--cgroup",
		fmt.Sprintf("cpuset.mems=%v", numaNode),
		"--cgroup",
		fmt.Sprintf("cpuset.cpus=%s", cpus),
		"--id",
		id,
		"--exec-file",
		firecrackerBin,
		"--",
		"--api-sock",
		FirecrackerSocketName,
	)

	if enableOutput {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	if enableInput {
		cmd.Stdin = os.Stdin
	} else {
		// Don't forward CTRL-C etc. signals from parent to child process
		// We can't enable this if we set the cmd stdin or we deadlock
		cmd.SysProcAttr = &unix.SysProcAttr{
			Setpgid: true,
			Pgid:    0,
		}
	}

	if err := cmd.Start(); err != nil {
		panic(errors.Join(ErrCouldNotStartFirecrackerServer, err))
	}
	server.VMPid = cmd.Process.Pid

	var closeLock sync.Mutex
	closed := false

	// We can only run this once since `cmd.Wait()` releases resources after the first call
	server.Wait = sync.OnceValue(func() error {
		if err := cmd.Wait(); err != nil {
			closeLock.Lock()
			defer closeLock.Unlock()

			if closed && (err.Error() == errSignalKilled.Error()) { // Don't treat killed errors as errors if we killed the process
				return nil
			}

			return errors.Join(ErrFirecrackerExited, err)
		}

		return nil
	})

	// We intentionally don't call `wg.Add` and `wg.Done` here - we are ok with leaking this
	// goroutine since we return the process, which allows tracking errors and stopping this goroutine
	// and waiting for it to be stopped. We still need to `defer handleGoroutinePanic()()` however so that
	// any errors we get as we're polling the socket path directory are caught
	// It's important that we start this _after_ calling `cmd.Start`, otherwise our process would be nil
	goroutineManager.StartBackgroundGoroutine(func() {
		if err := server.Wait(); err != nil {
			panic(errors.Join(ErrCouldNotWaitForFirecracker, err))
		}
	})

	goroutineManager.StartForegroundGoroutine(func() {
		// Cause the `range Watcher.Event` loop to break if context is cancelled, e.g. when command errors
		<-goroutineManager.GetGoroutineCtx().Done()

		if err := watcher.Close(); err != nil {
			panic(errors.Join(ErrCouldNotCloseWatcher, err))
		}
	})

	// We intentionally don't call `wg.Add` and `wg.Done` here - we are ok with leaking this
	// goroutine since we return the process, which allows tracking errors and stopping this goroutine
	// and waiting for it to be stopped. We still need to `defer handleGoroutinePanic()()` however so that
	// if we cancel the context during this call, we still handle it appropriately
	goroutineManager.StartBackgroundGoroutine(func() {
		// Cause the Firecracker process to be closed if context is cancelled - cancelling `ctx` on the `exec.Command`
		// doesn't actually stop it, it only stops trying to start it!
		<-ctx.Done() // We use ctx, not internalCtx here since this resource outlives the function call

		if err := server.Close(); err != nil {
			panic(errors.Join(ErrCouldNotCloseServer, err))
		}
	})

	socketCreated := false

	socketPath := filepath.Join(server.VMPath, FirecrackerSocketName)
	for ev := range watcher.Event {
		if filepath.Clean(ev.Name) == filepath.Clean(socketPath) {
			socketCreated = true

			break
		}
	}

	if !socketCreated {
		panic(ErrNoSocketCreated)
	}

	server.Close = func() error {
		if cmd.Process != nil {
			closeLock.Lock()

			// We can't trust `cmd.Process != nil` - without this check we could get `os.ErrProcessDone` here on the second `Kill()` call
			if !closed {
				closed = true

				if err := cmd.Process.Kill(); err != nil {
					closeLock.Unlock()

					return err
				}
			}

			closeLock.Unlock()
		}

		return server.Wait()
	}

	return
}
