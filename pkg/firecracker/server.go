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
	"golang.org/x/sys/unix"
	"k8s.io/utils/inotify"
)

var (
	ErrNoSocketCreated = errors.New("no socket created")

	errFinished = errors.New("finished")

	errSignalKilled = errors.New("signal: killed")

	ErrFirecrackerExitedUnexpectedly = errors.New("firecracker exited unexpectedly")
)

const (
	FirecrackerSocketName = "firecracker.sock"
)

type FirecrackerServer struct {
	VMPath string
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

	var errsLock sync.Mutex

	var wg sync.WaitGroup
	defer wg.Wait()

	internalCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(errFinished)

	handleGoroutinePanic := func() func() {
		return func() {
			if err := recover(); err != nil {
				errsLock.Lock()
				defer errsLock.Unlock()

				var e error
				if v, ok := err.(error); ok {
					e = v
				} else {
					e = fmt.Errorf("%v", err)
				}

				if !(errors.Is(e, context.Canceled) && errors.Is(context.Cause(internalCtx), errFinished)) {
					errs = errors.Join(errs, e)
				}

				cancel(errFinished)
			}
		}
	}

	defer handleGoroutinePanic()()

	id := shortuuid.New()

	server.VMPath = filepath.Join(chrootBaseDir, "firecracker", id, "root")
	if err := os.MkdirAll(server.VMPath, os.ModePerm); err != nil {
		panic(err)
	}

	watcher, err := inotify.NewWatcher()
	if err != nil {
		panic(err)
	}
	defer watcher.Close()

	if err := watcher.AddWatch(server.VMPath, inotify.InCreate); err != nil {
		panic(err)
	}

	cpus, err := os.ReadFile(filepath.Join("/sys", "devices", "system", "node", fmt.Sprintf("node%v", numaNode), "cpulist"))
	if err != nil {
		panic(err)
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
		panic(err)
	}

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

			return errors.Join(ErrFirecrackerExitedUnexpectedly, err)
		}

		return nil
	})

	// We intentionally don't call `wg.Add` and `wg.Done` here - we are ok with leaking this
	// goroutine since we return the process, which allows tracking errors and stopping this goroutine
	// and waiting for it to be stopped. We still need to `defer handleGoroutinePanic()()` however so that
	// any errors we get as we're polling the socket path directory are caught
	// It's important that we start this _after_ calling `cmd.Start`, otherwise our process would be nil
	go func() {
		defer handleGoroutinePanic()()

		if err := server.Wait(); err != nil {
			panic(err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer handleGoroutinePanic()()

		// Cause the `range Watcher.Event` loop to break if context is cancelled, e.g. when command errors
		<-internalCtx.Done()

		if err := watcher.Close(); err != nil {
			panic(err)
		}
	}()

	// We intentionally don't call `wg.Add` and `wg.Done` here - we are ok with leaking this
	// goroutine since we return the process, which allows tracking errors and stopping this goroutine
	// and waiting for it to be stopped. We still need to `defer handleGoroutinePanic()()` however so that
	// if we cancel the context during this call, we still handle it appropriately
	go func() {
		defer handleGoroutinePanic()()

		// Cause the Firecracker to be closed if context is cancelled - cancelling `ctx` on the `exec.Command`
		// doesn't actually stop it, it only stops trying to start it!
		<-ctx.Done() // We use ctx, not internalCtx here since this resource outlives the function call

		if err := server.Close(); err != nil {
			panic(err)
		}
	}()

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

			if err := cmd.Process.Kill(); err != nil {
				closeLock.Unlock()

				return err
			}

			closed = true

			closeLock.Unlock()
		}

		return server.Wait()
	}

	return
}
