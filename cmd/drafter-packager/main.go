package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/loopholelabs/drafter/pkg/common"
	"github.com/loopholelabs/logging"
)

func main() {
	defDevices := make([]common.PackagerDevice, 0)
	for _, n := range common.KnownNames {
		defDevices = append(defDevices, common.PackagerDevice{
			Name: n,
			Path: filepath.Join("out", "package", common.DeviceFilenames[n]),
		})
	}

	defaultDevices, err := json.Marshal(defDevices)
	if err != nil {
		panic(err)
	}

	rawDevices := flag.String("devices", string(defaultDevices), "Devices configuration")
	packagePath := flag.String("package-path", filepath.Join("out", "app.tar.zst"), "Path to package file")
	extract := flag.Bool("extract", false, "Whether to extract or archive")
	flag.Parse()

	log := logging.New(logging.Zerolog, "drafter", os.Stderr)

	var devices []common.PackagerDevice
	if err := json.Unmarshal([]byte(*rawDevices), &devices); err != nil {
		panic(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt)
	go func() {
		<-done
		log.Info().Msg("Exiting gracefully")
		cancel()
	}()

	if *extract {
		err := common.ExtractPackage(ctx, *packagePath, devices)
		if err != nil {
			log.Error().Err(err).Msg("extract package failed")
			panic(err)
		}
	} else {
		err = common.ArchivePackage(ctx, devices, *packagePath)
		if err != nil {
			log.Error().Err(err).Msg("archive package failed")
			panic(err)
		}
	}
}
