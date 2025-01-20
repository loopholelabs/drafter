package main

import (
	"context"
	"flag"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/loopholelabs/drafter/pkg/common"
	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/drafter/pkg/mounter"
	"github.com/loopholelabs/drafter/pkg/packager"
	"github.com/loopholelabs/drafter/pkg/peer"
	"github.com/loopholelabs/drafter/pkg/runner"
	"github.com/loopholelabs/drafter/pkg/snapshotter"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
	"github.com/loopholelabs/logging"
	"github.com/loopholelabs/logging/types"
	"github.com/loopholelabs/silo/pkg/storage/migrator"
)

func main() {

	// General flags
	rawDevices := flag.String("devices", getDefaultDevices(), "Devices configuration")

	raddr := flag.String("raddr", "localhost:1337", "Remote address to connect to (leave empty to disable)")
	laddr := flag.String("laddr", "localhost:1337", "Local address to listen on (leave empty to disable)")
	concurrency := flag.Int("concurrency", 1024, "Number of concurrent workers to use in migrations")

	// Firewcracker flags
	rawFirecrackerBin := flag.String("firecracker-bin", "firecracker", "Firecracker binary")
	rawJailerBin := flag.String("jailer-bin", "jailer", "Jailer binary (from Firecracker)")
	chrootBaseDir := flag.String("chroot-base-dir", filepath.Join("out", "vms"), "chroot base directory")
	uid := flag.Int("uid", 0, "User ID for the Firecracker process")
	gid := flag.Int("gid", 0, "Group ID for the Firecracker process")
	enableOutput := flag.Bool("enable-output", true, "Whether to enable VM stdout and stderr")
	enableInput := flag.Bool("enable-input", false, "Whether to enable VM stdin")
	netns := flag.String("netns", "ark0", "Network namespace to run Firecracker in")
	numaNode := flag.Int("numa-node", 0, "NUMA node to run Firecracker in")
	cgroupVersion := flag.Int("cgroup-version", 2, "Cgroup version to use for Jailer")

	resumeTimeout := flag.Duration("resume-timeout", time.Minute, "Maximum amount of time to wait for agent and liveness to resume")
	rescueTimeout := flag.Duration("rescue-timeout", time.Minute, "Maximum amount of time to wait for rescue operations")

	experimentalMapPrivate := flag.Bool("experimental-map-private", false, "(Experimental) Whether to use MAP_PRIVATE for memory and state devices")
	experimentalMapPrivateStateOutput := flag.String("experimental-map-private-state-output", "", "(Experimental) Path to write the local changes to the shared state to (leave empty to write back to device directly) (ignored unless --experimental-map-private)")
	experimentalMapPrivateMemoryOutput := flag.String("experimental-map-private-memory-output", "", "(Experimental) Path to write the local changes to the shared memory to (leave empty to write back to device directly) (ignored unless --experimental-map-private)")

	flag.Parse()

	devices, err := decodeDevices(*rawDevices)
	if err != nil {
		panic(err)
	}

	// FIXME: Allow tweak from cmdline
	log := logging.New(logging.Zerolog, "drafter", os.Stderr)
	log.SetLevel(types.DebugLevel)

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

	bubbleSignals := false

	// Handle Interrupts
	done := make(chan os.Signal, 1)
	go func() {
		signal.Notify(done, os.Interrupt)
		v := <-done

		if bubbleSignals {
			done <- v
			return
		}

		log.Info().Msg("Exiting gracefully")
		cancel()
	}()

	firecrackerBin, err := exec.LookPath(*rawFirecrackerBin)
	if err != nil {
		panic(err)
	}

	jailerBin, err := exec.LookPath(*rawJailerBin)
	if err != nil {
		panic(err)
	}

	p, err := peer.StartPeer(
		goroutineManager.Context(),
		context.Background(), // Never give up on rescue operations

		snapshotter.HypervisorConfiguration{
			FirecrackerBin: firecrackerBin,
			JailerBin:      jailerBin,
			ChrootBaseDir:  *chrootBaseDir,
			UID:            *uid,
			GID:            *gid,
			NetNS:          *netns,
			NumaNode:       *numaNode,
			CgroupVersion:  *cgroupVersion,
			EnableOutput:   *enableOutput,
			EnableInput:    *enableInput,
		},

		packager.StateName,
		packager.MemoryName,
		log,
	)

	defer func() {
		defer goroutineManager.CreateForegroundPanicCollector()()
		if err := p.Wait(); err != nil {
			panic(err)
		}
	}()

	if err != nil {
		panic(err)
	}

	defer func() {
		defer goroutineManager.CreateForegroundPanicCollector()()
		if err := p.Close(); err != nil {
			panic(err)
		}
	}()

	goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
		if err := p.Wait(); err != nil {
			panic(err)
		}
	})

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
		closer, readers, writers, remoteAddr, err = connectAddr(goroutineManager.Context(), *raddr)
		if err != nil {
			panic(err)
		}
		defer closer.Close()

		log.Info().Str("remote", remoteAddr).Msg("Migrating from")
	}

	migratedPeer, err := p.MigrateFrom(
		goroutineManager.Context(),
		migrateFromDevices,
		readers,
		writers,
		mounter.MigrateFromHooks{
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

	defer func() {
		defer goroutineManager.CreateForegroundPanicCollector()()
		if err := migratedPeer.Wait(); err != nil {
			panic(err)
		}
	}()

	if err != nil {
		panic(err)
	}

	defer func() {
		defer goroutineManager.CreateForegroundPanicCollector()()
		if err := migratedPeer.Close(); err != nil {
			panic(err)
		}
	}()

	goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
		if err := migratedPeer.Wait(); err != nil {
			panic(err)
		}
	})

	before := time.Now()

	resumedPeer, err := migratedPeer.Resume(
		goroutineManager.Context(),
		*resumeTimeout,
		*rescueTimeout,
		struct{}{},
		ipc.AgentServerAcceptHooks[ipc.AgentServerRemote[struct{}], struct{}]{},
		runner.SnapshotLoadConfiguration{
			ExperimentalMapPrivate:             *experimentalMapPrivate,
			ExperimentalMapPrivateStateOutput:  *experimentalMapPrivateStateOutput,
			ExperimentalMapPrivateMemoryOutput: *experimentalMapPrivateMemoryOutput,
		},
	)

	if err != nil {
		panic(err)
	}

	defer func() {
		defer goroutineManager.CreateForegroundPanicCollector()()
		if err := resumedPeer.Close(); err != nil {
			panic(err)
		}
	}()

	goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
		if err := resumedPeer.Wait(); err != nil {
			panic(err)
		}
	})

	log.Info().Int64("ms", time.Since(before).Milliseconds()).Str("path", p.VMPath).Msg("Resumed VM")

	if err := migratedPeer.Wait(); err != nil {
		panic(err)
	}

	if strings.TrimSpace(*laddr) == "" {
		bubbleSignals = true

		select {
		case <-goroutineManager.Context().Done():
			return

		case <-done:
			before = time.Now()

			if err := resumedPeer.SuspendAndCloseAgentServer(goroutineManager.Context(), *resumeTimeout); err != nil {
				panic(err)
			}

			log.Info().Int64("ms", time.Since(before).Milliseconds()).Msg("Suspend. Shutting down.")
			return
		}
	}

	log.Info().Str("addr", *laddr).Msg("Listening for connections")
	closer, readers, writers, remoteAddr, err = listenAddr(goroutineManager.Context(), *laddr)
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
	if err := resumedPeer.MigrateTo(
		goroutineManager.Context(),
		migrateToDevices,
		*resumeTimeout,
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
	); err != nil {
		panic(err)
	}

	log.Info().Msg("Shutting down")
}
