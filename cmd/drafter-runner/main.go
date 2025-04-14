package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/loopholelabs/drafter/pkg/common"
	"github.com/loopholelabs/drafter/pkg/ipc"
	rfirecracker "github.com/loopholelabs/drafter/pkg/runtimes/firecracker"
	"github.com/loopholelabs/drafter/pkg/utils"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
	"github.com/loopholelabs/logging"
	"golang.org/x/sys/unix"
)

var (
	ErrCouldNotGetDeviceStat = errors.New("could not get NBD device stat")
)

type SharableDevice struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Shared bool   `json:"shared"`
}

func main() {
	log := logging.New(logging.Zerolog, "drafter", os.Stderr)

	defDevices := make([]SharableDevice, 0)
	for _, n := range common.KnownNames {
		defDevices = append(defDevices, SharableDevice{
			Name:   n,
			Path:   filepath.Join("out", "package", common.DeviceFilenames[n]),
			Shared: false,
		})
	}
	defaultDevices, err := json.Marshal(defDevices)
	if err != nil {
		panic(err)
	}

	rawFirecrackerBin := flag.String("firecracker-bin", "firecracker", "Firecracker binary")
	rawJailerBin := flag.String("jailer-bin", "jailer", "Jailer binary (from Firecracker)")

	chrootBaseDir := flag.String("chroot-base-dir", filepath.Join("out", "vms"), "chroot base directory")

	uid := flag.Int("uid", 0, "User ID for the Firecracker process")
	gid := flag.Int("gid", 0, "Group ID for the Firecracker process")

	enableOutput := flag.Bool("enable-output", true, "Whether to enable VM stdout and stderr")
	enableInput := flag.Bool("enable-input", false, "Whether to enable VM stdin")

	netns := flag.String("netns", "ark0", "Network namespace to run Firecracker in")

	numaNode := flag.Int("numa-node", 0, "NUMA node to run Firecracker in")
	cgroupVersion := flag.Int("cgroup-version", 2, "Cgroup version to use for Jailer")

	rawDevices := flag.String("devices", string(defaultDevices), "Devices configuration")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var devices []SharableDevice
	if err := json.Unmarshal([]byte(*rawDevices), &devices); err != nil {
		panic(err)
	}

	firecrackerBin, err := exec.LookPath(*rawFirecrackerBin)
	if err != nil {
		panic(err)
	}

	jailerBin, err := exec.LookPath(*rawJailerBin)
	if err != nil {
		panic(err)
	}

	configPath := ""
	for _, device := range devices {
		if device.Name == common.DeviceConfigName {
			configPath = device.Path

			break
		}
	}

	if strings.TrimSpace(configPath) == "" {
		panic(rfirecracker.ErrConfigFileNotFound)
	}

	configFile, err := os.Open(configPath)
	if err != nil {
		panic(err)
	}
	defer configFile.Close()

	var packageConfig rfirecracker.PackageConfiguration
	if err := json.NewDecoder(configFile).Decode(&packageConfig); err != nil {
		panic(err)
	}

	_ = configFile.Close()

	var errs error
	defer func() {
		if errs != nil {
			panic(errs)
		}
	}()

	goroutineManager := manager.NewGoroutineManager(
		ctx,
		&errs,
		manager.GoroutineManagerHooks{},
	)
	defer goroutineManager.Wait()
	defer goroutineManager.StopAllGoroutines()
	defer goroutineManager.CreateBackgroundPanicCollector()()

	bubbleSignals := false

	done := make(chan os.Signal, 1)
	go func() {
		signal.Notify(done, os.Interrupt)

		v := <-done

		if bubbleSignals {
			done <- v

			return
		}

		log.Info().Msg("Exiting gracefully")

		cancel()
	}()

	fcconfig := &rfirecracker.FirecrackerMachineConfig{
		FirecrackerBin: firecrackerBin,
		JailerBin:      jailerBin,

		ChrootBaseDir: *chrootBaseDir,

		UID: *uid,
		GID: *gid,

		NetNS:         *netns,
		NumaNode:      *numaNode,
		CgroupVersion: *cgroupVersion,

		EnableInput: *enableInput,
	}

	if *enableOutput {
		fcconfig.Stdout = os.Stdout
		fcconfig.Stderr = os.Stderr
	}

	m, err := rfirecracker.StartFirecrackerMachine(

		goroutineManager.Context(),
		log,
		fcconfig,
	)

	defer func() {
		defer goroutineManager.CreateForegroundPanicCollector()()

		if err := m.Wait(); err != nil {
			panic(err)
		}
	}()

	if err != nil {
		panic(err)
	}

	defer func() {
		defer goroutineManager.CreateForegroundPanicCollector()()

		if err := m.Close(); err != nil {
			panic(err)
		}
		err = os.RemoveAll(filepath.Dir(m.VMPath))
		if err != nil {
			panic(err)
		}
	}()

	goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
		if err := m.Wait(); err != nil {
			panic(err)
		}
	})

	for index, device := range devices {
		log.Info().Int("index", index).Str("name", device.Name).Msg("Requested local device")

		defer func() {
			defer goroutineManager.CreateForegroundPanicCollector()()

			resourceInfo, err := os.Stat(device.Path)
			if err != nil {
				panic(err)
			}

			if paddingLength := utils.GetBlockDevicePadding(resourceInfo.Size()); paddingLength > 0 {
				resourceFile, err := os.OpenFile(device.Path, os.O_WRONLY|os.O_APPEND, os.ModePerm)
				if err != nil {
					panic(err)
				}
				defer resourceFile.Close()

				if _, err := resourceFile.Write(make([]byte, paddingLength)); err != nil {
					panic(err)
				}
			}
		}()

		devicePath := ""
		if device.Shared {
			devicePath = device.Path
		} else {
			mnt := utils.NewLoopMount(device.Path)

			defer mnt.Close()
			devicePath, err = mnt.Open()
			if err != nil {
				panic(err)
			}
		}

		log.Info().Int("index", index).Str("devicePath", devicePath).Msg("Exposed local device")

		deviceInfo, err := os.Stat(devicePath)
		if err != nil {
			panic(err)
		}

		deviceStat, ok := deviceInfo.Sys().(*syscall.Stat_t)
		if !ok {
			panic(ErrCouldNotGetDeviceStat)
		}

		deviceMajor := uint64(deviceStat.Rdev / 256)
		deviceMinor := uint64(deviceStat.Rdev % 256)

		deviceID := int((deviceMajor << 8) | deviceMinor)

		select {
		case <-goroutineManager.Context().Done():
			if err := goroutineManager.Context().Err(); err != nil {
				panic(err)
			}

			return

		default:
			if err := unix.Mknod(filepath.Join(m.VMPath, device.Name), unix.S_IFBLK|0666, deviceID); err != nil {
				panic(err)
			}
		}
	}

	before := time.Now()

	resumeSnapshotAndAcceptCtx, cancelResumeSnapshotAndAcceptCtx := context.WithTimeout(ctx, 10*time.Second)
	defer cancelResumeSnapshotAndAcceptCtx()

	err = m.ResumeSnapshot(resumeSnapshotAndAcceptCtx, common.DeviceStateName, common.DeviceMemoryName)
	if err != nil {
		panic(err)
	}

	// Start the RPC stuff...
	agent, err := ipc.StartAgentRPC[struct{}, ipc.AgentServerRemote[struct{}]](
		log, path.Join(m.VMPath, rfirecracker.VSockName),
		packageConfig.AgentVSockPort, struct{}{})
	if err != nil {
		panic(err)
	}

	// Call after resume RPC
	afterResumeCtx, cancelAfterResumeCtx := context.WithTimeout(ctx, 10*time.Second)
	defer cancelAfterResumeCtx()

	r, err := agent.GetRemote(afterResumeCtx)
	if err != nil {
		panic(err)
	}

	remote := *(*ipc.AgentServerRemote[struct{}])(unsafe.Pointer(&r))
	err = remote.AfterResume(afterResumeCtx)
	if err != nil {
		panic(err)
	}

	defer func() {
		defer goroutineManager.CreateForegroundPanicCollector()()

		if err := agent.Close(); err != nil {
			panic(err)
		}
	}()

	log.Info().Str("vmpath", m.VMPath).Int64("ms", time.Since(before).Milliseconds()).Msg("Resumed VM")

	bubbleSignals = true

	select {
	case <-goroutineManager.Context().Done():
		return

	case <-done:
		break
	}

	before = time.Now()

	suspendCtx, cancelSuspendCtx := context.WithTimeout(ctx, 10*time.Second)
	defer cancelSuspendCtx()

	r, err = agent.GetRemote(suspendCtx)
	if err != nil {
		panic(err)
	}

	remote = *(*ipc.AgentServerRemote[struct{}])(unsafe.Pointer(&r))
	err = remote.BeforeSuspend(suspendCtx)
	if err != nil {
		panic(err)
	}

	err = agent.Close()
	if err != nil {
		panic(err)
	}

	if log != nil {
		log.Debug().Msg("resumedRunner createSnapshot")
	}

	err = m.CreateSnapshot(suspendCtx, common.DeviceStateName, "", rfirecracker.SDKSnapshotTypeMsyncAndState)
	if err != nil {
		panic(err)
	}

	log.Info().Msg("Shutting down")
}
