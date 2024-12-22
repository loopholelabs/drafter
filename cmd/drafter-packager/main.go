package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/loopholelabs/drafter/pkg/packager"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
)

func main() {
	defaultDevices, err := json.Marshal([]packager.PackagerDevice{
		{
			Name: packager.StateName,
			Path: filepath.Join("out", "package", "state.bin"),
		},
		{
			Name: packager.MemoryName,
			Path: filepath.Join("out", "package", "memory.bin"),
		},

		{
			Name: packager.KernelName,
			Path: filepath.Join("out", "package", "vmlinux"),
		},
		{
			Name: packager.DiskName,
			Path: filepath.Join("out", "package", "rootfs.ext4"),
		},

		{
			Name: packager.ConfigName,
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

	var devices []packager.PackagerDevice
	if err := json.Unmarshal([]byte(*rawDevices), &devices); err != nil {
		panic(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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

	go func() {
		done := make(chan os.Signal, 1)
		signal.Notify(done, os.Interrupt)

		<-done

		log.Println("Exiting gracefully")

		cancel()
	}()

	if *extract {
		if err := packager.ExtractPackage(
			goroutineManager.Context(),

			*packagePath,
			devices,

			packager.PackagerHooks{
				OnBeforeProcessFile: func(name, path string) {
					log.Println("Extracting device", name, "to", path)
				},
			},
		); err != nil {
			panic(err)
		}

		return
	}

	if err := packager.ArchivePackage(
		goroutineManager.Context(),

		devices,
		*packagePath,

		packager.PackagerHooks{
			OnBeforeProcessFile: func(name, path string) {
				log.Println("Archiving device", name, "from", path)
			},
		},
	); err != nil {
		panic(err)
	}
}
