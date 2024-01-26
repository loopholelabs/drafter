package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/loopholelabs/drafter/pkg/config"
	"github.com/loopholelabs/drafter/pkg/roles"
	"github.com/loopholelabs/drafter/pkg/utils"
	"golang.org/x/sys/unix"
)

func main() {
	rawFirecrackerBin := flag.String("firecracker-bin", "firecracker", "Firecracker binary")
	rawJailerBin := flag.String("jailer-bin", "jailer", "Jailer binary (from Firecracker)")

	chrootBaseDir := flag.String("chroot-base-dir", filepath.Join("out", "vms"), "`chroot` base directory")

	uid := flag.Int("uid", 0, "User ID for the Firecracker process")
	gid := flag.Int("gid", 0, "Group ID for the Firecracker process")

	enableOutput := flag.Bool("enable-output", true, "Whether to enable VM stdout and stderr")
	enableInput := flag.Bool("enable-input", false, "Whether to enable VM stdin")

	resumeTimeout := flag.Duration("resume-timeout", time.Minute, "Maximum amount of time to wait for agent to resume")

	netns := flag.String("netns", "ark0", "Network namespace to run Firecracker in")

	numaNode := flag.Int("numa-node", 0, "NUMA node to run Firecracker in")
	cgroupVersion := flag.Int("cgroup-version", 2, "Cgroup version to use for Jailer")

	statePath := flag.String("state-path", filepath.Join("out", "package", "drafter.drftstate"), "State path")
	memoryPath := flag.String("memory-path", filepath.Join("out", "package", "drafter.drftmemory"), "Memory path")
	initramfsPath := flag.String("initramfs-path", filepath.Join("out", "package", "drafter.drftinitramfs"), "initramfs path")
	kernelPath := flag.String("kernel-path", filepath.Join("out", "package", "drafter.drftkernel"), "Kernel path")
	diskPath := flag.String("disk-path", filepath.Join("out", "package", "drafter.drftdisk"), "Disk path")
	configPath := flag.String("config-path", filepath.Join("out", "package", "drafter.drftconfig"), "Config path")

	copy := flag.Bool("copy", false, "Whether to copy the package into the VM directory instead of using loop mounts")
	persist := flag.Bool("persist", true, "Whether to write back changes to the package after stopping the VM (always true for loop mounts)")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	firecrackerBin, err := exec.LookPath(*rawFirecrackerBin)
	if err != nil {
		panic(err)
	}

	jailerBin, err := exec.LookPath(*rawJailerBin)
	if err != nil {
		panic(err)
	}

	configFile, err := os.Open(*configPath)
	if err != nil {
		panic(err)
	}
	defer configFile.Close()

	var packageConfig config.PackageConfiguration
	if err := json.NewDecoder(configFile).Decode(&packageConfig); err != nil {
		panic(err)
	}

	_ = configFile.Close()

	runner := roles.NewRunner(
		config.HypervisorConfiguration{
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

		config.StateName,
		config.MemoryName,
	)

	var wg sync.WaitGroup
	defer wg.Wait()

	wg.Add(1)
	go func() {
		defer wg.Done()

		if err := runner.Wait(); err != nil {
			panic(err)
		}
	}()

	defer runner.Close()
	vmPath, err := runner.Open()
	if err != nil {
		panic(err)
	}

	resources := [][2]string{
		{
			config.InitramfsName,
			*initramfsPath,
		},
		{
			config.KernelName,
			*kernelPath,
		},
		{
			config.DiskName,
			*diskPath,
		},

		{
			config.StateName,
			*statePath,
		},
		{
			config.MemoryName,
			*memoryPath,
		},
	}
	for _, resource := range resources {
		if *copy {
			inputFile, err := os.Open(resource[1])
			if err != nil {
				panic(err)
			}
			defer inputFile.Close()

			if err := os.MkdirAll(filepath.Dir(filepath.Join(vmPath, resource[0])), os.ModePerm); err != nil {
				panic(err)
			}

			outputFile, err := os.OpenFile(filepath.Join(vmPath, resource[0]), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.ModePerm)
			if err != nil {
				panic(err)
			}
			defer outputFile.Close()

			if _, err = io.Copy(outputFile, inputFile); err != nil {
				panic(err)
			}

			_ = inputFile.Close()
			_ = outputFile.Close()

			if *persist {
				defer func(resource [2]string) {
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

					_ = inputFile.Close()
					_ = outputFile.Close()
				}(resource)
			}
		} else {
			defer func() {
				resourceInfo, err := os.Stat(resource[1])
				if err != nil {
					panic(err)
				}

				if paddingLength := utils.GetBlockDevicePadding(resourceInfo.Size()); paddingLength > 0 {
					resourceFile, err := os.OpenFile(resource[1], os.O_WRONLY|os.O_APPEND, os.ModePerm)
					if err != nil {
						panic(err)
					}
					defer resourceFile.Close()

					if _, err := resourceFile.Write(make([]byte, paddingLength)); err != nil {
						panic(err)
					}
				}
			}()

			mnt := utils.NewLoopMount(resource[1])

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

			if err := unix.Mknod(filepath.Join(vmPath, resource[0]), unix.S_IFBLK|0666, dev); err != nil {
				panic(err)
			}
		}
	}

	before := time.Now()

	if err := runner.Resume(ctx, *resumeTimeout, packageConfig.AgentVSockPort); err != nil {
		panic(err)
	}

	log.Println("Resume:", time.Since(before))

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt)

	<-done

	before = time.Now()

	if err := runner.Suspend(ctx, *resumeTimeout); err != nil {
		panic(err)
	}

	log.Println("Suspend:", time.Since(before))
}
