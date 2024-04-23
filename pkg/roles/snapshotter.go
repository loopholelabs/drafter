package roles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/loopholelabs/drafter/pkg/config"
	"github.com/loopholelabs/drafter/pkg/firecracker"
	"github.com/loopholelabs/drafter/pkg/utils"
	"github.com/loopholelabs/drafter/pkg/vsock"
)

var (
	ErrCouldNotGetDeviceStat = errors.New("could not get NBD device stat")
)

func CreateSnapshot(
	ctx context.Context,

	initramfsInputPath string,
	kernelInputPath string,
	diskInputPath string,

	stateOutputPath string,
	memoryOutputPath string,
	initramfsOutputPath string,
	kernelOutputPath string,
	diskOutputPath string,
	configOutputPath string,

	vmConfiguration config.VMConfiguration,
	livenessConfiguration config.LivenessConfiguration,

	hypervisorConfiguration config.HypervisorConfiguration,
	networkConfiguration config.NetworkConfiguration,
	agentConfiguration config.AgentConfiguration,

	knownNamesConfiguration config.KnownNamesConfiguration,
) (errs []error) {
	var errsLock sync.Mutex

	var wg sync.WaitGroup
	defer wg.Wait()

	ctx, cancel := context.WithCancelCause(ctx)
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

				if !(errors.Is(e, context.Canceled) && errors.Is(context.Cause(ctx), errFinished)) {
					errs = append(errs, e)
				}

				cancel(errFinished)
			}
		}
	}

	defer handleGoroutinePanic()()

	if err := os.MkdirAll(hypervisorConfiguration.ChrootBaseDir, os.ModePerm); err != nil {
		panic(err)
	}

	vmPath, firecrackerWait, firecrackerKill, errs := firecracker.StartFirecrackerServer(
		ctx,

		hypervisorConfiguration.FirecrackerBin,
		hypervisorConfiguration.JailerBin,

		hypervisorConfiguration.ChrootBaseDir,

		hypervisorConfiguration.UID,
		hypervisorConfiguration.GID,

		hypervisorConfiguration.NetNS,
		hypervisorConfiguration.NumaNode,
		hypervisorConfiguration.CgroupVersion,

		hypervisorConfiguration.EnableOutput,
		hypervisorConfiguration.EnableInput,
	)
	for _, err := range errs {
		if err != nil {
			panic(err)
		}
	}
	defer firecrackerKill()
	defer os.RemoveAll(filepath.Dir(vmPath)) // Remove `firecracker/$id`, not just `firecracker/$id/root`

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer handleGoroutinePanic()()

		if err := firecrackerWait(); err != nil {
			panic(err)
		}
	}()

	ping := vsock.NewLivenessPingReceiver(
		filepath.Join(vmPath, VSockName),
		uint32(livenessConfiguration.LivenessVSockPort),
	)

	livenessVSockPath, err := ping.Open()
	if err != nil {
		panic(err)
	}
	defer ping.Close()

	if err := os.Chown(livenessVSockPath, hypervisorConfiguration.UID, hypervisorConfiguration.GID); err != nil {
		panic(err)
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.Dial("unix", filepath.Join(vmPath, firecracker.FirecrackerSocketName))
			},
		},
	}

	wg.Add(1)
	defer func() {
		defer wg.Done()
		defer handleGoroutinePanic()()

		for _, resource := range [][2]string{
			{
				knownNamesConfiguration.InitramfsName,
				initramfsOutputPath,
			},
			{
				knownNamesConfiguration.KernelName,
				kernelOutputPath,
			},
			{
				knownNamesConfiguration.DiskName,
				diskOutputPath,
			},

			{
				knownNamesConfiguration.StateName,
				stateOutputPath,
			},
			{
				knownNamesConfiguration.MemoryName,
				memoryOutputPath,
			},

			{
				knownNamesConfiguration.ConfigName,
				configOutputPath,
			},
		} {
			inputFile, err := os.Open(filepath.Join(vmPath, resource[0]))
			if err != nil {
				panic(err)
			}
			defer inputFile.Close()

			if err := os.MkdirAll(filepath.Dir(resource[1]), os.ModePerm); err != nil {
				panic(err)
			}

			outputFile, err := os.OpenFile(resource[1], os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.ModePerm)
			if err != nil {
				panic(err)
			}
			defer outputFile.Close()

			resourceSize, err := io.Copy(outputFile, inputFile)
			if err != nil {
				panic(err)
			}

			if paddingLength := utils.GetBlockDevicePadding(resourceSize); paddingLength > 0 {
				if _, err := outputFile.Write(make([]byte, paddingLength)); err != nil {
					panic(err)
				}
			}
		}
	}()
	// We need to stop the Firecracker process from using the mount before we can unmount it
	defer firecrackerKill()

	var (
		initramfsWorkingPath = filepath.Join(vmPath, knownNamesConfiguration.InitramfsName)
		kernelWorkingPath    = filepath.Join(vmPath, knownNamesConfiguration.KernelName)
		diskWorkingPath      = filepath.Join(vmPath, knownNamesConfiguration.DiskName)
	)

	if _, err := utils.CopyFile(initramfsInputPath, initramfsWorkingPath, hypervisorConfiguration.UID, hypervisorConfiguration.GID); err != nil {
		panic(err)
	}

	if _, err := utils.CopyFile(kernelInputPath, kernelWorkingPath, hypervisorConfiguration.UID, hypervisorConfiguration.GID); err != nil {
		panic(err)
	}

	if _, err := utils.CopyFile(diskInputPath, diskWorkingPath, hypervisorConfiguration.UID, hypervisorConfiguration.GID); err != nil {
		panic(err)
	}

	if err := firecracker.StartVM(
		client,

		knownNamesConfiguration.InitramfsName,
		knownNamesConfiguration.KernelName,
		knownNamesConfiguration.DiskName,

		vmConfiguration.CPUCount,
		vmConfiguration.MemorySize,
		vmConfiguration.CPUTemplate,
		vmConfiguration.BootArgs,

		networkConfiguration.Interface,
		networkConfiguration.MAC,

		VSockName,
		vsock.CIDGuest,
	); err != nil {
		panic(err)
	}
	defer os.Remove(filepath.Join(vmPath, VSockName))

	if err := ping.ReceiveAndClose(ctx); err != nil {
		panic(err)
	}

	handler := vsock.NewHandler(
		filepath.Join(vmPath, VSockName),
		uint32(agentConfiguration.AgentVSockPort),
	)

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer handleGoroutinePanic()()

		if err := handler.Wait(); err != nil {
			panic(err)
		}
	}()

	defer handler.Close()
	remote, err := handler.Open(ctx, time.Millisecond*100, agentConfiguration.ResumeTimeout)
	if err != nil {
		panic(err)
	}

	{
		ctx, cancel := context.WithTimeout(ctx, agentConfiguration.ResumeTimeout)
		defer cancel()

		if err := remote.BeforeSuspend(ctx); err != nil {
			panic(err)
		}
	}

	// Connections need to be closed before creating the snapshot
	ping.Close()
	_ = handler.Close()

	if err := firecracker.CreateSnapshot(
		client,

		knownNamesConfiguration.StateName,
		knownNamesConfiguration.MemoryName,

		firecracker.SnapshotTypeFull,
	); err != nil {
		panic(err)
	}

	packageConfig, err := json.Marshal(config.PackageConfiguration{
		AgentVSockPort: agentConfiguration.AgentVSockPort,
	})
	if err != nil {
		panic(err)
	}

	if _, err := utils.WriteFile(packageConfig, filepath.Join(vmPath, knownNamesConfiguration.ConfigName), hypervisorConfiguration.UID, hypervisorConfiguration.GID); err != nil {
		panic(err)
	}

	return
}
