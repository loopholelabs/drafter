package firecracker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	sdk "github.com/firecracker-microvm/firecracker-go-sdk"
	loggingtypes "github.com/loopholelabs/logging/types"
	"github.com/sirupsen/logrus"

	"github.com/lithammer/shortuuid/v4"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
	"golang.org/x/sys/unix"
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
	log    loggingtypes.Logger
	VMPath string
	VMPid  int

	closed    bool
	closeLock sync.Mutex

	cmd      *exec.Cmd
	cmdWg    sync.WaitGroup
	cmdErr   error
	cmdErrCh chan error
}

/**
 * Close the server
 *
 */
func (fs *FirecrackerServer) Close() error {
	if fs.log != nil {
		fs.log.Info().Str("VMPath", fs.VMPath).Msg("Firecracker server closing")
	}

	if fs.cmd.Process != nil {
		fs.closeLock.Lock()

		if !fs.closed {
			fs.closed = true
			err := fs.cmd.Process.Kill()
			if err != nil {
				fs.closeLock.Unlock()
				return err
			}
		}
		fs.closeLock.Unlock()
	}

	// Wait for the process to exit
	fs.cmdWg.Wait()

	// We killed it! We expect this
	if fs.cmdErr.Error() == errSignalKilled.Error() {
		return nil
	}

	// Something else bad happened
	return fs.cmdErr
}

/**
 * Wait for the firecracker command to exit, and return any error
 *
 */
func (fs *FirecrackerServer) Wait() error {
	fs.cmdWg.Wait()
	err := fs.cmdErr

	if err != nil {
		fs.closeLock.Lock()
		defer fs.closeLock.Unlock()
		if fs.closed && (err.Error() == errSignalKilled.Error()) { // Don't treat killed errors as errors if we killed the process
			return nil
		}
		return errors.Join(ErrFirecrackerExited, err)
	}
	return nil
}

/**
 * Start a new firecracker server
 *
 */
func StartFirecrackerServer(ctx context.Context, log loggingtypes.Logger, firecrackerBin string, jailerBin string,
	chrootBaseDir string, uid int, gid int, netns string, numaNode int,
	cgroupVersion int, enableOutput bool, enableInput bool) (server *FirecrackerServer, errs error) {

	if log != nil {
		log.Info().Msg("Starting firecracker server")
	}

	server = &FirecrackerServer{
		cmdErrCh: make(chan error, 1),
		log:      log,
	}

	id := shortuuid.New()

	server.VMPath = filepath.Join(chrootBaseDir, "firecracker", id, "root")
	err := os.MkdirAll(server.VMPath, os.ModePerm)
	if err != nil {
		return nil, errors.Join(ErrCouldNotCreateVMPathDirectory, err)
	}

	if log != nil {
		log.Debug().Str("VMPath", server.VMPath).Msg("firecracker VMPath created")
	}

	jBuilder := sdk.NewJailerCommandBuilder().
		WithBin(jailerBin).
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

	err = server.cmd.Start()
	if err != nil {
		return nil, errors.Join(ErrCouldNotStartFirecrackerServer, err)
	}
	server.VMPid = server.cmd.Process.Pid
	// Wait for the process to finish, and report any error
	server.cmdWg.Add(1)
	go func() {
		server.cmdErr = server.cmd.Wait()
		server.cmdWg.Done()
		server.closeLock.Lock()
		defer server.closeLock.Unlock()
		if server.closed && (server.cmdErr.Error() == errSignalKilled.Error()) { // Don't treat killed errors as errors if we killed the process
			server.cmdErrCh <- nil
		} else {
			server.cmdErrCh <- server.cmdErr
		}
	}()

	if log != nil {
		log.Debug().Int("VMPid", server.VMPid).Msg("Started firecracker server")
	}

	// TODO: Tidy up under here...

	goroutineManager := manager.NewGoroutineManager(
		ctx,
		&errs,
		manager.GoroutineManagerHooks{},
	)
	defer goroutineManager.Wait()
	defer goroutineManager.StopAllGoroutines()
	defer goroutineManager.CreateBackgroundPanicCollector()()

	// If the context is cancelled, shut down the server
	goroutineManager.StartBackgroundGoroutine(func(_ context.Context) {
		// Cause the Firecracker process to be closed if context is cancelled - cancelling `ctx` on the `exec.Command`
		// doesn't actually stop it, it only stops trying to start it!
		<-ctx.Done() // We use ctx, not goroutineManager.Context() here since this resource outlives the function call

		if err := server.Close(); err != nil {
			panic(errors.Join(ErrCouldNotCloseServer, err))
		}
	})

	socketPath := filepath.Join(server.VMPath, FirecrackerSocketName)

	ticker := time.NewTicker(10 * time.Millisecond)
	waitCtx, waitCancel := context.WithTimeout(ctx, time.Second*10)

	defer func() {
		waitCancel()
		ticker.Stop()
	}()

	logger := logrus.New()
	clog := logger.WithField("subsystem", "firecracker")
	clog.WithField("socketPath", socketPath).Info("Loading snapshot")
	client := sdk.NewClient(socketPath, clog, true)

	for {
		select {
		case <-waitCtx.Done():
			return nil, errors.Join(ErrNoSocketCreated, waitCtx.Err())
		case e := <-server.cmdErrCh: // The cmd exited already. Return it as an error here.
			return nil, e
		case <-ticker.C:
			_, err := os.Stat(socketPath)
			if err != nil {
				continue
			}

			// Send test HTTP request to make sure socket is available
			_, err = client.GetMachineConfiguration()
			if err != nil {
				continue
			}

			if log != nil {
				log.Info().Str("VMPath", server.VMPath).Msg("Firecracker server up and ready")
			}
			return server, nil
		}
	}
}
