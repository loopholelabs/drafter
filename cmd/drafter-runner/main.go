package main

import (
	"context"
	"encoding/json"
	"errors"
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
	"golang.org/x/sys/unix"
)

var (
	errConfigFileNotFound = errors.New("config file not found")
)

func main() {
	defaultDevices, err := json.Marshal([]roles.PackagerDevice{
		{
			Name: roles.StateName,
			Path: filepath.Join("out", "package", "drafter.drftstate"),
		},
		{
			Name: roles.MemoryName,
			Path: filepath.Join("out", "package", "drafter.drftmemory"),
		},

		{
			Name: roles.InitramfsName,
			Path: filepath.Join("out", "package", "drafter.drftinitramfs"),
		},
		{
			Name: roles.KernelName,
			Path: filepath.Join("out", "package", "drafter.drftkernel"),
		},
		{
			Name: roles.DiskName,
			Path: filepath.Join("out", "package", "drafter.drftdisk"),
		},

		{
			Name: roles.ConfigName,
			Path: filepath.Join("out", "package", "drafter.drftconfig"),
		},

		{
			Name: "oci",
			Path: filepath.Join("out", "blueprint", "drafter.drftoci"),
		},
	})
	if err != nil {
		panic(err)
	}

	rawFirecrackerBin := flag.String("firecracker-bin", "firecracker", "Firecracker binary")
	rawJailerBin := flag.String("jailer-bin", "jailer", "Jailer binary (from Firecracker)")

	chrootBaseDir := flag.String("chroot-base-dir", filepath.Join("out", "vms"), "`chroot` base directory")

	uid := flag.Int("uid", 0, "User ID for the Firecracker process")
	gid := flag.Int("gid", 0, "Group ID for the Firecracker process")

	enableOutput := flag.Bool("enable-output", true, "Whether to enable VM stdout and stderr")
	enableInput := flag.Bool("enable-input", false, "Whether to enable VM stdin")

	resumeTimeout := flag.Duration("resume-timeout", time.Minute, "Maximum amount of time to wait for agent and liveness to resume")
	rescueTimeout := flag.Duration("rescue-timeout", time.Second*5, "Maximum amount of time to wait for rescue operations")

	netns := flag.String("netns", "ark0", "Network namespace to run Firecracker in")

	numaNode := flag.Int("numa-node", 0, "NUMA node to run Firecracker in")
	cgroupVersion := flag.Int("cgroup-version", 2, "Cgroup version to use for Jailer")

	rawDevices := flag.String("devices", string(defaultDevices), "Devices configuration")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var devices []roles.PackagerDevice
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

	ctx, handlePanics, handleGoroutinePanics, cancel, wait, _ := utils.GetPanicHandler(
		ctx,
		&errs,
		utils.GetPanicHandlerHooks{},
	)
	defer wait()
	defer cancel()
	defer handlePanics(false)()

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
		ctx,
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
			defer handlePanics(true)()

			if err := runner.Wait(); err != nil {
				panic(err)
			}
		}()
	}

	if err != nil {
		panic(err)
	}

	defer func() {
		defer handlePanics(true)()

		if err := runner.Close(); err != nil {
			panic(err)
		}
	}()

	handleGoroutinePanics(true, func() {
		if err := runner.Wait(); err != nil {
			panic(err)
		}
	})

	for _, device := range devices {
		defer func() {
			defer handlePanics(true)()

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

		mnt := utils.NewLoopMount(device.Path)

		defer mnt.Close()
		file, err := mnt.Open()
		if err != nil {
			panic(err)
		}

		info, err := os.Stat(file)
		if err != nil {
			panic(err)
		}

		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			panic(roles.ErrCouldNotGetDeviceStat)
		}

		major := uint64(stat.Rdev / 256)
		minor := uint64(stat.Rdev % 256)

		dev := int((major << 8) | minor)

		select {
		case <-ctx.Done():
			if err := ctx.Err(); err != nil {
				panic(ctx.Err())
			}

			return

		default:
			if err := unix.Mknod(filepath.Join(runner.VMPath, device.Name), unix.S_IFBLK|0666, dev); err != nil {
				panic(err)
			}
		}
	}

	before := time.Now()

	resumedRunner, err := runner.Resume(
		ctx,

		*resumeTimeout,
		*rescueTimeout,
		packageConfig.AgentVSockPort,
	)

	if err != nil {
		panic(err)
	}

	defer func() {
		defer handlePanics(true)()

		if err := resumedRunner.Close(); err != nil {
			panic(err)
		}
	}()

	handleGoroutinePanics(true, func() {
		if err := resumedRunner.Wait(); err != nil {
			panic(err)
		}
	})

	log.Println("Resumed VM in", time.Since(before), "on", runner.VMPath)

	bubbleSignals = true

	select {
	case <-ctx.Done():
		return

	case <-done:
		break
	}

	before = time.Now()

	if err := resumedRunner.SuspendAndCloseAgentServer(ctx, *resumeTimeout); err != nil {
		panic(err)
	}

	log.Println("Suspend:", time.Since(before))

	log.Println("Shutting down")
}
