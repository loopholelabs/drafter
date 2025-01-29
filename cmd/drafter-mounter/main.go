package main

import (
	"context"
	"flag"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/loopholelabs/drafter/internal/cmd"
	"github.com/loopholelabs/drafter/pkg/common"
	"github.com/loopholelabs/drafter/pkg/peer"
	"github.com/loopholelabs/drafter/pkg/runtimes"
	"github.com/loopholelabs/logging"
	"github.com/loopholelabs/logging/types"
	"github.com/loopholelabs/silo/pkg/storage/migrator"
)

func main() {

	// General flags
	rawDevices := flag.String("devices", cmd.GetDefaultDevices(), "Devices configuration")

	raddr := flag.String("raddr", "localhost:1337", "Remote address to connect to (leave empty to disable)")
	laddr := flag.String("laddr", "localhost:1337", "Local address to listen on (leave empty to disable)")
	concurrency := flag.Int("concurrency", 1024, "Number of concurrent workers to use in migrations")
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

	go func() {
		err := <-p.BackgroundErr()
		log.Error().Err(err).Msg("Error from peer background tasks")
		cancel()
	}()

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

	before := time.Now()

	log.Info().Int64("ms", time.Since(before).Milliseconds()).Str("path", p.VMPath).Msg("Resumed VM")

	if strings.TrimSpace(*laddr) != "" {

		log.Info().Str("addr", *laddr).Msg("Listening for connections")
		closer, readers, writers, remoteAddr, err = cmd.ListenAddr(ctx, *laddr)
		if err != nil {
			if err == context.Canceled {
				// We don't care
			} else {
				log.Error().Err(err).Msg("Error listening for connection")
				panic(err)
			}
		} else {
			defer closer.Close()

			log.Info().Str("addr", remoteAddr).Msg("Migrating to")

			migrateToDevices := []common.MigrateToDevice{}
			for _, device := range devices {
				if !device.MakeMigratable || device.Shared {
					continue
				}

				migrateToDevices = append(migrateToDevices, common.MigrateToDevice{
					Name:           device.Name,
					MaxDirtyBlocks: device.MaxDirtyBlocks,
					MinCycles:      device.MinCycles,
					MaxCycles:      device.MaxCycles,
					CycleThrottle:  device.CycleThrottle,
				})
			}

			before = time.Now()
			err = p.MigrateTo(
				ctx,
				migrateToDevices,
				1*time.Second,
				*concurrency,
				readers,
				writers,
				peer.MigrateToHooks{
					OnBeforeSuspend: func() {
						before = time.Now()
					},
					OnAfterSuspend: func() {
						log.Info().Int64("ms", time.Since(before).Milliseconds()).Msg("Suspend")
					},
					OnAllMigrationsCompleted: func() {
						log.Info().Msg("Completed all device migrations")
					},
					OnProgress: func(p map[string]*migrator.MigrationProgress) {
						totalSize := 0
						totalDone := 0
						for _, prog := range p {
							totalSize += (prog.TotalBlocks * prog.BlockSize)
							totalDone += (prog.ReadyBlocks * prog.BlockSize)
						}

						perc := float64(0.0)
						if totalSize > 0 {
							perc = float64(totalDone) * 100 / float64(totalSize)
						}
						// Report overall migration progress
						log.Info().Float64("perc", perc).
							Int("done", totalDone).
							Int("size", totalSize).
							Msg("# Overall migration Progress #")

						// Report individual devices
						for name, prog := range p {
							log.Info().Str("name", name).
								Int("migratedBlocks", prog.MigratedBlocks).
								Int("totalBlocks", prog.TotalBlocks).
								Int("readyBlocks", prog.ReadyBlocks).
								Int("totalMigratedBlocks", prog.TotalMigratedBlocks).
								Msg("Device migration progress")
						}

					},
				},
			)
			if err != nil {
				log.Error().Err(err).Msg("Error migrating to")
				panic(err)
			}
			cancel() // Cancel the context, since we're done.
		}
	}

	// Wait for context done
	<-ctx.Done()

	log.Info().Msg("Shutting down")
}
