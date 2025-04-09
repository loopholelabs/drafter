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

	sdk "github.com/loopholelabs/firecracker-go-sdk"
	loggingtypes "github.com/loopholelabs/logging/types"
	"github.com/sirupsen/logrus"

	"github.com/lithammer/shortuuid/v4"
	"golang.org/x/sys/unix"

	"github.com/loopholelabs/firecracker-go-sdk/client/models"
)

var (
	ErrCouldNotSetBootSource        = errors.New("could not set boot source")
	ErrCouldNotSetDrive             = errors.New("could not set drive")
	ErrCouldNotSetMachineConfig     = errors.New("could not set machine config")
	ErrCouldNotSetVSock             = errors.New("could not set vsock")
	ErrCouldNotSetNetworkInterfaces = errors.New("could not set network interfaces")
	ErrCouldNotStartInstance        = errors.New("could not start instance")
)

const SDKSnapshotTypeFull = "Full"
const SDKSnapshotTypeMsync = "Msync"
const SDKSnapshotTypeMsyncAndState = "MsyncAndState"
const SDKActionInstanceStart = "InstanceStart"
const SDKIOEngineSync = "Sync"
const SDKIOEngineAsync = "Async"
const SDKVMStatePaused = "Paused"
const SDKVMStateRunning = "Running"
const SDKBackendTypeFile = "File"

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

type FirecrackerMachineConfig struct {
	FirecrackerBin string
	JailerBin      string
	ChrootBaseDir  string
	UID            int
	GID            int
	NetNS          string
	NumaNode       int
	CgroupVersion  int
	EnableOutput   bool
	EnableInput    bool
}

type NetworkConfiguration struct {
	Interface string
	MAC       string
}

type VMConfiguration struct {
	CPUCount    int64
	MemorySize  int64
	CPUTemplate string
	BootArgs    string
}

type FirecrackerMachine struct {
	Conf   *FirecrackerMachineConfig
	log    loggingtypes.Logger
	VMPath string
	VMPid  int

	closed    bool
	closeLock sync.Mutex

	cmd      *exec.Cmd
	cmdWg    sync.WaitGroup
	cmdErr   error
	cmdErrCh chan error

	client *sdk.Client
}

/**
 * Start a new firecracker machine
 *
 */
func StartFirecrackerMachine(ctx context.Context, log loggingtypes.Logger, conf *FirecrackerMachineConfig) (*FirecrackerMachine, error) {

	if log != nil {
		log.Info().Msg("Starting firecracker machine")
	}

	server := &FirecrackerMachine{
		cmdErrCh: make(chan error, 1),
		log:      log,
		Conf:     conf,
	}

	id := shortuuid.New()

	server.VMPath = filepath.Join(conf.ChrootBaseDir, "firecracker", id, "root")
	err := os.MkdirAll(server.VMPath, os.ModePerm)
	if err != nil {
		return nil, errors.Join(ErrCouldNotCreateVMPathDirectory, err)
	}

	if log != nil {
		log.Debug().Str("VMPath", server.VMPath).Msg("firecracker VMPath created")
	}

	jBuilder := sdk.NewJailerCommandBuilder().
		WithBin(conf.JailerBin).
		WithChrootBaseDir(conf.ChrootBaseDir).
		WithUID(conf.UID).
		WithGID(conf.GID).
		WithNetNS(filepath.Join("/var", "run", "netns", conf.NetNS)).
		WithCgroupVersion(fmt.Sprintf("%v", conf.CgroupVersion)).
		WithNumaNode(conf.NumaNode).
		WithID(id).
		WithExecFile(conf.FirecrackerBin).
		WithFirecrackerArgs("--api-sock", FirecrackerSocketName)

	if conf.EnableOutput {
		jBuilder = jBuilder.
			WithStdout(os.Stdout).
			WithStderr(os.Stderr)
	}
	if conf.EnableInput {
		jBuilder = jBuilder.
			WithStdin(os.Stdin)
	}

	// Create the command
	server.cmd = jBuilder.Build(ctx)

	if !conf.EnableInput {
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
		log.Debug().Int("VMPid", server.VMPid).Msg("Started firecracker machine")
	}

	// If the context is cancelled, close the server.
	go func() {
		<-ctx.Done()
		err := server.Close()
		if err != nil {
			if server.log != nil {
				server.log.Error().Err(err).Msg("close fc error after context cancelled")
			}
		}
	}()

	socketPath := filepath.Join(server.VMPath, FirecrackerSocketName)

	ticker := time.NewTicker(10 * time.Millisecond)
	waitCtx, waitCancel := context.WithTimeout(ctx, time.Second*10)

	defer func() {
		waitCancel()
		ticker.Stop()
	}()

	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)
	clog := logger.WithField("subsystem", "firecracker")
	clog.WithField("socketPath", socketPath).Info("Loading snapshot")
	server.client = sdk.NewClient(socketPath, clog, true)

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
			_, err = server.client.GetMachineConfiguration()
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

/**
 * Close the server
 *
 */
func (fs *FirecrackerMachine) Close() error {
	if fs.log != nil {
		fs.log.Info().Str("VMPath", fs.VMPath).Msg("Firecracker machine closing")
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
func (fs *FirecrackerMachine) Wait() error {
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
 * Resume from a snapshot.
 *
 */
func (fs *FirecrackerMachine) ResumeSnapshot(ctx context.Context, statePath string, memoryPath string) error {

	_, err := fs.client.LoadSnapshot(ctx, &models.SnapshotLoadParams{
		EnableDiffSnapshots: false,
		MemBackend: &models.MemoryBackend{
			BackendType: sdk.String(SDKBackendTypeFile),
			BackendPath: &memoryPath,
		},
		ResumeVM:     true,
		SnapshotPath: &statePath,
		Shared:       true,
	})

	if err != nil {
		return errors.Join(ErrCouldNotResumeSnapshot, err)
	}

	return nil
}

/**
 * Pause the VM and create a snapshot.
 *
 */
func (fs *FirecrackerMachine) CreateSnapshot(ctx context.Context, statePath string, memoryPath string, snapshotType string) error {

	if snapshotType != SDKSnapshotTypeMsync {
		_, err := fs.client.PatchVM(ctx, &models.VM{
			State: sdk.String(SDKVMStatePaused),
		})
		if err != nil {
			return err
		}
	}

	_, err := fs.client.CreateSnapshot(ctx, &models.SnapshotCreateParams{
		MemFilePath:  &memoryPath,
		SnapshotPath: &statePath,
		SnapshotType: snapshotType,
	})

	return err
}

/**
 * Start VM
 *
 */
func (fs *FirecrackerMachine) StartVM(ctx context.Context, kernelPath string,
	disks []string, ioEngine string,
	vmConfig *VMConfiguration, netConfig *NetworkConfiguration,
	vsockPath string, vsockCID int64) error {

	_, err := fs.client.PutGuestBootSource(ctx, &models.BootSource{
		KernelImagePath: &kernelPath,
		BootArgs:        vmConfig.BootArgs,
	})
	if err != nil {
		return errors.Join(ErrCouldNotSetBootSource, err)
	}

	for _, disk := range disks {
		_, err := fs.client.PutGuestDriveByID(ctx, disk, &models.Drive{
			DriveID:      &disk,
			IoEngine:     &ioEngine,
			PathOnHost:   &disk,
			IsRootDevice: sdk.Bool(false),
			IsReadOnly:   sdk.Bool(false),
		})
		if err != nil {
			return errors.Join(ErrCouldNotSetDrive, err)
		}
	}

	_, err = fs.client.PutMachineConfiguration(ctx, &models.MachineConfiguration{
		VcpuCount:   &vmConfig.CPUCount,
		MemSizeMib:  &vmConfig.MemorySize,
		CPUTemplate: models.CPUTemplate(vmConfig.CPUTemplate).Pointer(),
	})
	if err != nil {
		return errors.Join(ErrCouldNotSetMachineConfig, err)
	}

	_, err = fs.client.PutGuestVsock(ctx, &models.Vsock{
		GuestCid: &vsockCID,
		UdsPath:  &vsockPath,
	})

	if err != nil {
		return errors.Join(ErrCouldNotSetVSock, err)
	}

	_, err = fs.client.PutGuestNetworkInterfaceByID(ctx, netConfig.Interface, &models.NetworkInterface{
		IfaceID:     &netConfig.Interface,
		GuestMac:    netConfig.MAC,
		HostDevName: &netConfig.Interface,
	})

	if err != nil {
		return errors.Join(ErrCouldNotSetNetworkInterfaces, err)
	}

	_, err = fs.client.CreateSyncAction(ctx, &models.InstanceActionInfo{
		ActionType: sdk.String(SDKActionInstanceStart),
	})

	if err != nil {
		return errors.Join(ErrCouldNotStartInstance, err)
	}

	return nil
}
