package main

import (
	"encoding/json"
	"flag"
	"path/filepath"

	"github.com/loopholelabs/drafter/pkg/roles"
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

	rawDevices := flag.String("devices", string(defaultDevices), "Devices configuration")

	packagePath := flag.String("package-path", filepath.Join("out", "app.drafterpackage"), "Path to package file")

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
		); err != nil {
			panic(err)
		}

		return
	}

	if err := roles.ArchivePackage(
		devices,

		*packagePath,
	); err != nil {
		panic(err)
	}
}
