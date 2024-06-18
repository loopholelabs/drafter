package main

import (
	"encoding/json"
	"flag"
	"log"
	"path/filepath"

	"github.com/loopholelabs/drafter/pkg/roles"
)

func main() {
	defaultDevices, err := json.Marshal([]roles.PackagerDevice{
		{
			Name: roles.StateName,
			Path: filepath.Join("out", "package", "state.bin"),
		},
		{
			Name: roles.MemoryName,
			Path: filepath.Join("out", "package", "memory.bin"),
		},

		{
			Name: roles.KernelName,
			Path: filepath.Join("out", "package", "vmlinux"),
		},
		{
			Name: roles.DiskName,
			Path: filepath.Join("out", "package", "rootfs.ext4"),
		},

		{
			Name: roles.ConfigName,
			Path: filepath.Join("out", "package", "config.json"),
		},

		{
			Name: "oci",
			Path: filepath.Join("out", "blueprint", "oci.ext4"),
		},
	})
	if err != nil {
		panic(err)
	}

	rawDevices := flag.String("devices", string(defaultDevices), "Devices configuration")

	packagePath := flag.String("package-path", filepath.Join("out", "app.tar.zst"), "Path to package file")

	extract := flag.Bool("extract", false, "Whether to extract or archive")

	flag.Parse()

	var devices []roles.PackagerDevice
	if err := json.Unmarshal([]byte(*rawDevices), &devices); err != nil {
		panic(err)
	}

	if *extract {
		if err := roles.ExtractPackage(
			*packagePath,
			devices,

			roles.PackagerHooks{
				OnBeforeProcessFile: func(name string) {
					log.Println("Extracting file", name)
				},
			},
		); err != nil {
			panic(err)
		}

		return
	}

	if err := roles.ArchivePackage(
		devices,
		*packagePath,

		roles.PackagerHooks{
			OnBeforeProcessFile: func(name string) {
				log.Println("Archiving file", name)
			},
		},
	); err != nil {
		panic(err)
	}
}
