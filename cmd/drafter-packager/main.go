package main

import (
	"flag"
	"path/filepath"

	"github.com/loopholelabs/drafter/pkg/config"
	"github.com/loopholelabs/drafter/pkg/roles"
)

func main() {
	statePath := flag.String("state-path", filepath.Join("out", "package", "drafter.drftstate"), "State path")
	memoryPath := flag.String("memory-path", filepath.Join("out", "package", "drafter.drftmemory"), "Memory path")
	initramfsPath := flag.String("initramfs-path", filepath.Join("out", "package", "drafter.drftinitramfs"), "initramfs path")
	kernelPath := flag.String("kernel-path", filepath.Join("out", "package", "drafter.drftkernel"), "Kernel path")
	diskPath := flag.String("disk-path", filepath.Join("out", "package", "drafter.drftdisk"), "Disk path")
	configPath := flag.String("config-path", filepath.Join("out", "package", "drafter.drftconfig"), "Config path")

	packagePath := flag.String("package-path", filepath.Join("out", "redis.drft"), "Path to package file")

	extract := flag.Bool("extract", false, "Whether to extract or archive")

	flag.Parse()

	knownNamesConfiguration := config.KnownNamesConfiguration{
		InitramfsName: config.InitramfsName,
		KernelName:    config.KernelName,
		DiskName:      config.DiskName,

		StateName:  config.StateName,
		MemoryName: config.MemoryName,

		ConfigName: config.ConfigName,
	}

	if *extract {
		if err := roles.ExtractPackage(
			*packagePath,

			*statePath,
			*memoryPath,
			*initramfsPath,
			*kernelPath,
			*diskPath,
			*configPath,

			knownNamesConfiguration,
		); err != nil {
			panic(err)
		}

		return
	}

	if err := roles.ArchivePackage(
		*statePath,
		*memoryPath,
		*initramfsPath,
		*kernelPath,
		*diskPath,
		*configPath,

		*packagePath,

		knownNamesConfiguration,
	); err != nil {
		panic(err)
	}
}
