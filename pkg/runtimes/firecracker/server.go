package firecracker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	sdk "github.com/firecracker-microvm/firecracker-go-sdk"

	"github.com/lithammer/shortuuid/v4"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
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

	Wait func() error

	closed        bool
	closeLock     sync.Mutex
	socketCreated bool

	cmd *exec.Cmd
}

/**
 * Close the server
 *
 */
func (fs *FirecrackerServer) Close() error {
	if !fs.socketCreated {
		return nil
	}
	if fs.cmd.Process != nil {
		fs.closeLock.Lock()

		// We can't trust `cmd.Process != nil` - without this check we could get `os.ErrProcessDone` here on the second `Kill()` call
		if !fs.closed {
			fs.closed = true
			if err := fs.cmd.Process.Kill(); err != nil {
				fs.closeLock.Unlock()
				return err
			}
		}
		fs.closeLock.Unlock()
	}
	return fs.Wait()
}

func StartFirecrackerServer(ctx context.Context, firecrackerBin string, jailerBin string,
	chrootBaseDir string, uid int, gid int, netns string, numaNode int,
	cgroupVersion int, enableOutput bool, enableInput bool) (server *FirecrackerServer, errs error) {

	server = &FirecrackerServer{
		Wait: func() error {
			return nil
		},
	}

	id := shortuuid.New()

	server.VMPath = filepath.Join(chrootBaseDir, "firecracker", id, "root")
	if err := os.MkdirAll(server.VMPath, os.ModePerm); err != nil {
		return nil, errors.Join(ErrCouldNotCreateVMPathDirectory, err)
	}

	jBuilder := sdk.NewJailerCommandBuilder()
	jBuilder = jBuilder.WithBin(jailerBin).
		WithChrootBaseDir(chrootBaseDir).
		WithUID(uid).
		WithGID(gid).
		WithNetNS(filepath.Join("/var", "run", "netns", netns)).
		WithCgroupVersion(fmt.Sprintf("%v", cgroupVersion)).
		WithNumaNode(numaNode).
		WithID(id).
		WithExecFile(firecrackerBin).
		WithFirecrackerArgs("--api-sock", FirecrackerSocketName)

	if enableOutput {
		jBuilder = jBuilder.
			WithStdout(os.Stdout).
			WithStderr(os.Stderr)
	}
	if enableInput {
		jBuilder = jBuilder.
			WithStdin(os.Stdin)
	}

	// Create the command
	server.cmd = jBuilder.Build(ctx)

	if !enableInput {
		// Don't forward CTRL-C etc. signals from parent to child process
		// We can't enable this if we set the cmd stdin or we deadlock
		server.cmd.SysProcAttr = &unix.SysProcAttr{
			Setpgid: true,
			Pgid:    0,
		}
	}

	goroutineManager := manager.NewGoroutineManager(
		ctx,
		&errs,
		manager.GoroutineManagerHooks{},
	)
	defer goroutineManager.Wait()
	defer goroutineManager.StopAllGoroutines()
	defer goroutineManager.CreateBackgroundPanicCollector()()

	watcher, err := inotify.NewWatcher()
	if err != nil {
		return nil, errors.Join(ErrCouldNotCreateInotifyWatcher, err)
	}
	defer watcher.Close()

	if err := watcher.AddWatch(server.VMPath, inotify.InCreate); err != nil {
		return nil, errors.Join(ErrCouldNotAddInotifyWatch, err)
	}

	err = server.cmd.Start()
	if err != nil {
		return nil, errors.Join(ErrCouldNotStartFirecrackerServer, err)
	}
	server.VMPid = server.cmd.Process.Pid

	// We can only run this once since `cmd.Wait()` releases resources after the first call
	server.Wait = sync.OnceValue(func() error {
		if err := server.cmd.Wait(); err != nil {
			server.closeLock.Lock()
			defer server.closeLock.Unlock()

			if server.closed && (err.Error() == errSignalKilled.Error()) { // Don't treat killed errors as errors if we killed the process
				return nil
			}

			return errors.Join(ErrFirecrackerExited, err)
		}

		return nil
	})

	// It is safe to start a background goroutine here since we return a wait function
	// Despite returning a wait function, we still need to start this goroutine however so that any errors
	// we get as we're polling the socket path directory are caught
	// It's important that we start this _after_ calling `cmd.Start`, otherwise our process would be nil
	goroutineManager.StartBackgroundGoroutine(func(_ context.Context) {
		if err := server.Wait(); err != nil {
			panic(errors.Join(ErrCouldNotWaitForFirecracker, err))
		}
	})

	goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
		// Cause the `range Watcher.Event` loop to break if context is cancelled, e.g. when command errors
		<-goroutineManager.Context().Done()

		if err := watcher.Close(); err != nil {
			panic(errors.Join(ErrCouldNotCloseWatcher, err))
		}
	})

	// If the context is cancelled, shut down the server
	goroutineManager.StartBackgroundGoroutine(func(_ context.Context) {
		// Cause the Firecracker process to be closed if context is cancelled - cancelling `ctx` on the `exec.Command`
		// doesn't actually stop it, it only stops trying to start it!
		<-ctx.Done() // We use ctx, not goroutineManager.Context() here since this resource outlives the function call

		if err := server.Close(); err != nil {
			panic(errors.Join(ErrCouldNotCloseServer, err))
		}
	})

	server.socketCreated = false

	socketPath := filepath.Join(server.VMPath, FirecrackerSocketName)
	for ev := range watcher.Event {
		if filepath.Clean(ev.Name) == filepath.Clean(socketPath) {
			server.socketCreated = true
			break
		}
	}

	if !server.socketCreated {
		return nil, ErrNoSocketCreated
	}

	return
}
