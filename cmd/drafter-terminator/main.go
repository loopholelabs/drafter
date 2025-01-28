package main

import (
	"context"
	"flag"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/loopholelabs/drafter/internal/cmd"
	"github.com/loopholelabs/drafter/pkg/common"
	"github.com/loopholelabs/drafter/pkg/peer"
	"github.com/loopholelabs/drafter/pkg/runtimes"
	"github.com/loopholelabs/logging"
	"github.com/loopholelabs/logging/types"
)

func main() {

	// General flags
	rawDevices := flag.String("devices", cmd.GetDefaultDevices(), "Devices configuration")

	raddr := flag.String("raddr", "localhost:1337", "Remote address to connect to (leave empty to disable)")
	devpath := flag.String("path", "", "Path to expose devices (optional)")

	flag.Parse()

	devices, err := cmd.DecodeDevices(*rawDevices)
	if err != nil {
		panic(err)
	}

	// FIXME: Allow tweak from cmdline
	log := logging.New(logging.Zerolog, "drafter", os.Stderr)
	log.SetLevel(types.DebugLevel)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle Interrupt by cancelling context
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt)
	go func() {
		<-done
		log.Info().Msg("Exiting gracefully")
		cancel()
	}()

	rp := &runtimes.EmptyRuntimeProvider{
		HomePath: *devpath,
	}

	p, err := peer.StartPeer(ctx,
		context.Background(), // Never give up on rescue operations
		log, rp,
	)

	if err != nil {
		log.Error().Err(err).Msg("Could not start peer")
		panic(err)
	}

	defer func() {
		err := p.Close()
		if err != nil {
			panic(err)
		}
	}()

	migrateFromDevices := []common.MigrateFromDevice{}
	for _, device := range devices {
		migrateFromDevices = append(migrateFromDevices, common.MigrateFromDevice{
			Name:      device.Name,
			Base:      device.Base,
			Overlay:   device.Overlay,
			State:     device.State,
			BlockSize: device.BlockSize,
			Shared:    device.Shared,
		})
	}

	var readers []io.Reader
	var writers []io.Writer
	var closer io.Closer
	var remoteAddr string

	if *raddr != "" {
		closer, readers, writers, remoteAddr, err = cmd.ConnectAddr(ctx, *raddr)
		if err != nil {
			panic(err)
		}
		defer closer.Close()

		log.Info().Str("remote", remoteAddr).Msg("Migrating from")
	}

	before := time.Now()

	err = p.MigrateFrom(ctx, migrateFromDevices, readers, writers,
		peer.MigrateFromHooks{
			OnLocalDeviceRequested: func(localDeviceID uint32, name string) {
				log.Info().Uint32("deviceID", localDeviceID).Str("name", name).Msg("Requested local device")
			},
			OnLocalDeviceExposed: func(localDeviceID uint32, path string) {
				log.Info().Uint32("deviceID", localDeviceID).Str("path", path).Msg("Exposed local device")
			},
			OnLocalAllDevicesRequested: func() {
				log.Info().Msg("Requested all local devices")
			},
		},
	)

	if err != nil {
		log.Error().Err(err).Msg("Could not migrate peer")
		panic(err)
	}

	// Wait for any pending migration
	err = p.Wait()
	if err != nil {
		log.Error().Err(err).Msg("Error waiting for migration")
		panic(err)
	}

	log.Info().Int64("ms", time.Since(before).Milliseconds()).Str("path", p.VMPath).Msg("Resumed VM")

	// Wait for context done
	<-ctx.Done()

	log.Info().Msg("Shutting down")
}
