package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/loopholelabs/drafter/pkg/common"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
)

func main() {
	defaultDevices, err := json.Marshal([]common.PackagerDevice{
		{
			Name: common.DeviceStateName,
			Path: filepath.Join("out", "package", "state.bin"),
		},
		{
			Name: common.DeviceMemoryName,
			Path: filepath.Join("out", "package", "memory.bin"),
		},

		{
			Name: common.DeviceKernelName,
			Path: filepath.Join("out", "package", "vmlinux"),
		},
		{
			Name: common.DeviceDiskName,
			Path: filepath.Join("out", "package", "rootfs.ext4"),
		},

		{
			Name: common.DeviceConfigName,
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

	var devices []common.PackagerDevice
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
		if err := common.ExtractPackage(
			goroutineManager.Context(),

			*packagePath,
			devices,
		); err != nil {
			panic(err)
		}

		return
	}

	if err := common.ArchivePackage(
		goroutineManager.Context(),

		devices,
		*packagePath,
	); err != nil {
		panic(err)
	}
}
