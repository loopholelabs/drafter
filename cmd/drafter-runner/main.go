package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/loopholelabs/drafter/pkg/roles"
	"github.com/loopholelabs/drafter/pkg/utils"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
	"golang.org/x/sys/unix"
)

type SharableDevice struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Shared bool   `json:"shared"`
}

func main() {
	defaultDevices, err := json.Marshal([]SharableDevice{
		{
			Name:   roles.StateName,
			Path:   filepath.Join("out", "package", "state.bin"),
			Shared: false,
		},
		{
			Name:   roles.MemoryName,
			Path:   filepath.Join("out", "package", "memory.bin"),
			Shared: false,
		},

		{
			Name:   roles.KernelName,
			Path:   filepath.Join("out", "package", "vmlinux"),
			Shared: false,
		},
		{
			Name:   roles.DiskName,
			Path:   filepath.Join("out", "package", "rootfs.ext4"),
			Shared: false,
		},

		{
			Name:   roles.ConfigName,
			Path:   filepath.Join("out", "package", "config.json"),
			Shared: false,
		},

		{
			Name:   "oci",
			Path:   filepath.Join("out", "blueprint", "oci.ext4"),
			Shared: false,
		},
	})
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

	resumeTimeout := flag.Duration("resume-timeout", time.Minute, "Maximum amount of time to wait for agent and liveness to resume")
	rescueTimeout := flag.Duration("rescue-timeout", time.Second*5, "Maximum amount of time to wait for rescue operations")

	netns := flag.String("netns", "ark0", "Network namespace to run Firecracker in")

	numaNode := flag.Int("numa-node", 0, "NUMA node to run Firecracker in")
	cgroupVersion := flag.Int("cgroup-version", 2, "Cgroup version to use for Jailer")

	experimentalMapPrivate := flag.Bool("experimental-map-private", false, "(Experimental) Whether to use MAP_PRIVATE for memory and state devices")
	experimentalMapPrivateStateOutput := flag.String("experimental-map-private-state-output", "", "(Experimental) Path to write the local changes to the shared state to (leave empty to write back to device directly) (ignored unless --experimental-map-private)")
	experimentalMapPrivateMemoryOutput := flag.String("experimental-map-private-memory-output", "", "(Experimental) Path to write the local changes to the shared memory to (leave empty to write back to device directly) (ignored unless --experimental-map-private)")

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
		if device.Name == roles.ConfigName {
			configPath = device.Path

			break
		}
	}

	if strings.TrimSpace(configPath) == "" {
		panic(roles.ErrConfigFileNotFound)
	}

	configFile, err := os.Open(configPath)
	if err != nil {
		panic(err)
	}
	defer configFile.Close()

	var packageConfig roles.PackageConfiguration
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
	defer goroutineManager.WaitForForegroundGoroutines()
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

		log.Println("Exiting gracefully")

		cancel()
	}()

	runner, err := roles.StartRunner(
		goroutineManager.GetGoroutineCtx(),
		context.Background(), // Never give up on rescue operations

		roles.HypervisorConfiguration{
			FirecrackerBin: firecrackerBin,
			JailerBin:      jailerBin,

			ChrootBaseDir: *chrootBaseDir,

			UID: *uid,
			GID: *gid,

			NetNS:         *netns,
			NumaNode:      *numaNode,
			CgroupVersion: *cgroupVersion,

			EnableOutput: *enableOutput,
			EnableInput:  *enableInput,
		},

		roles.StateName,
		roles.MemoryName,
	)

	if runner.Wait != nil {
		defer func() {
			defer goroutineManager.CreateForegroundPanicCollector()()

			if err := runner.Wait(); err != nil {
				panic(err)
			}
		}()
	}

	if err != nil {
		panic(err)
	}

	defer func() {
		defer goroutineManager.CreateForegroundPanicCollector()()

		if err := runner.Close(); err != nil {
			panic(err)
		}
	}()

	goroutineManager.StartForegroundGoroutine(func() {
		if err := runner.Wait(); err != nil {
			panic(err)
		}
	})

	for index, device := range devices {
		log.Println("Requested local device", index, "with name", device.Name)

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

		log.Println("Exposed local device", index, "at", devicePath)

		deviceInfo, err := os.Stat(devicePath)
		if err != nil {
			panic(err)
		}

		deviceStat, ok := deviceInfo.Sys().(*syscall.Stat_t)
		if !ok {
			panic(roles.ErrCouldNotGetDeviceStat)
		}

		deviceMajor := uint64(deviceStat.Rdev / 256)
		deviceMinor := uint64(deviceStat.Rdev % 256)

		deviceID := int((deviceMajor << 8) | deviceMinor)

		select {
		case <-goroutineManager.GetGoroutineCtx().Done():
			if err := goroutineManager.GetGoroutineCtx().Err(); err != nil {
				panic(goroutineManager.GetGoroutineCtx().Err())
			}

			return

		default:
			if err := unix.Mknod(filepath.Join(runner.VMPath, device.Name), unix.S_IFBLK|0666, deviceID); err != nil {
				panic(err)
			}
		}
	}

	before := time.Now()

	resumedRunner, err := runner.Resume(
		goroutineManager.GetGoroutineCtx(),

		*resumeTimeout,
		*rescueTimeout,
		packageConfig.AgentVSockPort,

		roles.SnapshotLoadConfiguration{
			ExperimentalMapPrivate: *experimentalMapPrivate,

			ExperimentalMapPrivateStateOutput:  *experimentalMapPrivateStateOutput,
			ExperimentalMapPrivateMemoryOutput: *experimentalMapPrivateMemoryOutput,
		},
	)

	if err != nil {
		panic(err)
	}

	defer func() {
		defer goroutineManager.CreateForegroundPanicCollector()()

		if err := resumedRunner.Close(); err != nil {
			panic(err)
		}
	}()

	goroutineManager.StartForegroundGoroutine(func() {
		if err := resumedRunner.Wait(); err != nil {
			panic(err)
		}
	})

	log.Println("Resumed VM in", time.Since(before), "on", runner.VMPath)

	bubbleSignals = true

	select {
	case <-goroutineManager.GetGoroutineCtx().Done():
		return

	case <-done:
		break
	}

	before = time.Now()

	if err := resumedRunner.SuspendAndCloseAgentServer(goroutineManager.GetGoroutineCtx(), *resumeTimeout); err != nil {
		panic(err)
	}

	log.Println("Suspend:", time.Since(before))

	log.Println("Shutting down")
}
