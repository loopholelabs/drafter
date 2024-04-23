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

	errSignalKilled           = errors.New("signal: killed")
	errWaitNoChildProcesses   = errors.New("wait: no child processes")
	errWaitIDNoChildProcesses = errors.New("waitid: no child processes")
)

const (
	FirecrackerSocketName = "firecracker.sock"
)

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
) (vmDir string, waitFunc func() error, closeFunc func() error, errs []error) {
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
					errs = append(errs, e)
				}

				cancel(errFinished)
			}
		}
	}

	defer handleGoroutinePanic()()

	id := shortuuid.New()

	vmDir = filepath.Join(chrootBaseDir, "firecracker", id, "root")
	if err := os.MkdirAll(vmDir, os.ModePerm); err != nil {
		panic(err)
	}

	watcher, err := inotify.NewWatcher()
	if err != nil {
		panic(err)
	}
	defer watcher.Close()

	if err := watcher.AddWatch(vmDir, inotify.InCreate); err != nil {
		panic(err)
	}

	cpus, err := os.ReadFile(filepath.Join("/sys", "devices", "system", "node", fmt.Sprintf("node%v", numaNode), "cpulist"))
	if err != nil {
		panic(err)
	}

	cmd := exec.CommandContext(
		ctx,
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

	// We intentionally don't call `wg.Add` and `wg.Done` here - we are ok with leaking this
	// goroutine since we return the process, which allows tracking errors and stopping this goroutine
	// and waiting for it to be stopped. We still need to `defer handleGoroutinePanic()()` however so that
	// any errors we get as we're polling the socket path directory are caught
	go func() {
		defer handleGoroutinePanic()()

		if err := cmd.Run(); err != nil {
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

	socketCreated := false

	socketPath := filepath.Join(vmDir, FirecrackerSocketName)
	for ev := range watcher.Event {
		if filepath.Clean(ev.Name) == filepath.Clean(socketPath) {
			socketCreated = true

			break
		}
	}

	if !socketCreated {
		panic(ErrNoSocketCreated)
	}

	waitFunc = func() error {
		if cmd.Process != nil {
			if _, err := cmd.Process.Wait(); err != nil && err.Error() != errSignalKilled.Error() && err.Error() != errWaitNoChildProcesses.Error() && err.Error() != errWaitIDNoChildProcesses.Error() {
				return err
			}
		}

		return nil
	}

	closeFunc = func() error {
		if cmd.Process != nil {
			return cmd.Process.Kill()
		}

		return waitFunc()
	}

	return
}
