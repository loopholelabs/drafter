package mounter

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	iutils "github.com/loopholelabs/drafter/internal/utils"
	"github.com/loopholelabs/drafter/pkg/registry"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
	"github.com/loopholelabs/silo/pkg/storage"
	"github.com/loopholelabs/silo/pkg/storage/migrator"
	"github.com/loopholelabs/silo/pkg/storage/protocol"
	"github.com/loopholelabs/silo/pkg/storage/protocol/packets"
)

type MounterMigrateToHooks struct {
	OnBeforeGetDirtyBlocks func(deviceID uint32, remote bool)

	OnDeviceSent                       func(deviceID uint32, remote bool)
	OnDeviceAuthoritySent              func(deviceID uint32, remote bool)
	OnDeviceInitialMigrationProgress   func(deviceID uint32, remote bool, ready int, total int)
	OnDeviceContinousMigrationProgress func(deviceID uint32, remote bool, delta int)
	OnDeviceFinalMigrationProgress     func(deviceID uint32, remote bool, delta int)
	OnDeviceMigrationCompleted         func(deviceID uint32, remote bool)

	OnAllDevicesSent         func()
	OnAllMigrationsCompleted func()
}

type MigrateToDevice struct {
	Name string `json:"name"`

	MaxDirtyBlocks int `json:"maxDirtyBlocks"`
	MinCycles      int `json:"minCycles"`
	MaxCycles      int `json:"maxCycles"`

	CycleThrottle time.Duration `json:"cycleThrottle"`
}

type MigratableMounter struct {
	Close func()

	stage4Inputs []makeMigratableDeviceStage
}

func (migratableMounter *MigratableMounter) MigrateTo(
	ctx context.Context,

	devices []MigrateToDevice,

	concurrency int,

	readers []io.Reader,
	writers []io.Writer,

	hooks MounterMigrateToHooks,
) (errs error) {
	goroutineManager := manager.NewGoroutineManager(
		ctx,
		&errs,
		manager.GoroutineManagerHooks{},
	)
	defer goroutineManager.Wait()
	defer goroutineManager.StopAllGoroutines()
	defer goroutineManager.CreateBackgroundPanicCollector()()

	pro := protocol.NewProtocolRW(
		goroutineManager.Context(),
		readers,
		writers,
		nil,
	)

	goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
		if err := pro.Handle(); err != nil && !errors.Is(err, io.EOF) {
			panic(errors.Join(registry.ErrCouldNotHandleProtocol, err))
		}
	})

	var (
		devicesLeftToSend                 atomic.Int32
		devicesLeftToTransferAuthorityFor atomic.Int32

		suspendedVMLock sync.Mutex
		suspendedVM     bool
	)

	suspendedVMCh := make(chan struct{})

	suspendAndMsyncVM := sync.OnceValue(func() error {
		suspendedVMLock.Lock()
		suspendedVM = true
		suspendedVMLock.Unlock()

		close(suspendedVMCh) // We can safely close() this channel since the caller only runs once/is `sync.OnceFunc`d

		return nil
	})

	stage5Inputs := []migrateToStage{}
	for _, input := range migratableMounter.stage4Inputs {
		var migrateToDevice *MigrateToDevice
		for _, device := range devices {
			if device.Name == input.prev.prev.name {
				migrateToDevice = &device

				break
			}
		}

		// We don't want to serve this device
		if migrateToDevice == nil {
			continue
		}

		stage5Inputs = append(stage5Inputs, migrateToStage{
			prev: input,

			migrateToDevice: *migrateToDevice,
		})
	}

	_, deferFuncs, err := iutils.ConcurrentMap(
		stage5Inputs,
		func(index int, input migrateToStage, _ *struct{}, _ func(deferFunc func() error)) error {
			to := protocol.NewToProtocol(input.prev.storage.Size(), uint32(index), pro)

			if err := to.SendDevInfo(input.prev.prev.prev.name, input.prev.prev.prev.blockSize, ""); err != nil {
				return errors.Join(ErrCouldNotSendDevInfo, err)
			}

			if hook := hooks.OnDeviceSent; hook != nil {
				hook(uint32(index), input.prev.prev.prev.remote)
			}

			devicesLeftToSend.Add(1)
			if devicesLeftToSend.Load() >= int32(len(stage5Inputs)) {
				goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
					if err := to.SendEvent(&packets.Event{
						Type:       packets.EventCustom,
						CustomType: byte(registry.EventCustomAllDevicesSent),
					}); err != nil {
						panic(errors.Join(ErrCouldNotSendAllDevicesSentEvent, err))
					}

					if hook := hooks.OnAllDevicesSent; hook != nil {
						hook()
					}
				})
			}

			goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
				if err := to.HandleNeedAt(func(offset int64, length int32) {
					// Prioritize blocks
					endOffset := uint64(offset + int64(length))
					if endOffset > uint64(input.prev.storage.Size()) {
						endOffset = uint64(input.prev.storage.Size())
					}

					startBlock := int(offset / int64(input.prev.prev.prev.blockSize))
					endBlock := int((endOffset-1)/uint64(input.prev.prev.prev.blockSize)) + 1
					for b := startBlock; b < endBlock; b++ {
						input.prev.orderer.PrioritiseBlock(b)
					}
				}); err != nil {
					panic(errors.Join(registry.ErrCouldNotHandleNeedAt, err))
				}
			})

			goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
				if err := to.HandleDontNeedAt(func(offset int64, length int32) {
					// Deprioritize blocks
					endOffset := uint64(offset + int64(length))
					if endOffset > uint64(input.prev.storage.Size()) {
						endOffset = uint64(input.prev.storage.Size())
					}

					startBlock := int(offset / int64(input.prev.storage.Size()))
					endBlock := int((endOffset-1)/uint64(input.prev.storage.Size())) + 1
					for b := startBlock; b < endBlock; b++ {
						input.prev.orderer.Remove(b)
					}
				}); err != nil {
					panic(errors.Join(registry.ErrCouldNotHandleDontNeedAt, err))
				}
			})

			cfg := migrator.NewMigratorConfig().WithBlockSize(int(input.prev.prev.prev.blockSize))
			cfg.Concurrency = map[int]int{
				storage.BlockTypeAny:      concurrency,
				storage.BlockTypeStandard: concurrency,
				storage.BlockTypeDirty:    concurrency,
				storage.BlockTypePriority: concurrency,
			}
			cfg.Locker_handler = func() {
				defer goroutineManager.CreateBackgroundPanicCollector()()

				if err := to.SendEvent(&packets.Event{
					Type: packets.EventPreLock,
				}); err != nil {
					panic(errors.Join(ErrCouldNotSendPreLockEvent, err))
				}

				input.prev.storage.Lock()

				if err := to.SendEvent(&packets.Event{
					Type: packets.EventPostLock,
				}); err != nil {
					panic(errors.Join(ErrCouldNotSendPostLockEvent, err))
				}
			}
			cfg.Unlocker_handler = func() {
				defer goroutineManager.CreateBackgroundPanicCollector()()

				if err := to.SendEvent(&packets.Event{
					Type: packets.EventPreUnlock,
				}); err != nil {
					panic(errors.Join(ErrCouldNotSendPreUnlockEvent, err))
				}

				input.prev.storage.Unlock()

				if err := to.SendEvent(&packets.Event{
					Type: packets.EventPostUnlock,
				}); err != nil {
					panic(errors.Join(ErrCouldNotSendPostUnlockEvent, err))
				}
			}
			cfg.Error_handler = func(b *storage.BlockInfo, err error) {
				defer goroutineManager.CreateBackgroundPanicCollector()()

				if err != nil {
					panic(errors.Join(registry.ErrCouldNotContinueWithMigration, err))
				}
			}
			cfg.Progress_handler = func(p *migrator.MigrationProgress) {
				if hook := hooks.OnDeviceInitialMigrationProgress; hook != nil {
					hook(uint32(index), input.prev.prev.prev.remote, p.Ready_blocks, p.Total_blocks)
				}
			}

			mig, err := migrator.NewMigrator(input.prev.dirtyRemote, to, input.prev.orderer, cfg)
			if err != nil {
				return errors.Join(registry.ErrCouldNotCreateMigrator, err)
			}

			if err := mig.Migrate(input.prev.totalBlocks); err != nil {
				return errors.Join(ErrCouldNotMigrateBlocks, err)
			}

			if err := mig.WaitForCompletion(); err != nil {
				return errors.Join(registry.ErrCouldNotWaitForMigrationCompletion, err)
			}

			markDeviceAsReadyForAuthorityTransfer := sync.OnceFunc(func() {
				devicesLeftToTransferAuthorityFor.Add(1)
			})

			var (
				cyclesBelowDirtyBlockTreshold = 0
				totalCycles                   = 0
				ongoingMigrationsWg           sync.WaitGroup
			)
			for {
				ongoingMigrationsWg.Wait()

				if hook := hooks.OnBeforeGetDirtyBlocks; hook != nil {
					hook(uint32(index), input.prev.prev.prev.remote)
				}

				blocks := mig.GetLatestDirty()
				if blocks == nil {
					mig.Unlock()

					suspendedVMLock.Lock()
					if suspendedVM {
						suspendedVMLock.Unlock()

						break
					}
					suspendedVMLock.Unlock()
				}

				if blocks != nil {
					if err := to.DirtyList(int(input.prev.prev.prev.blockSize), blocks); err != nil {
						return errors.Join(ErrCouldNotSendDirtyList, err)
					}

					ongoingMigrationsWg.Add(1)
					goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
						defer ongoingMigrationsWg.Done()

						if err := mig.MigrateDirty(blocks); err != nil {
							panic(errors.Join(ErrCouldNotMigrateDirtyBlocks, err))
						}

						suspendedVMLock.Lock()
						defer suspendedVMLock.Unlock()

						if suspendedVM {
							if hook := hooks.OnDeviceFinalMigrationProgress; hook != nil {
								hook(uint32(index), input.prev.prev.prev.remote, len(blocks))
							}
						} else {
							if hook := hooks.OnDeviceContinousMigrationProgress; hook != nil {
								hook(uint32(index), input.prev.prev.prev.remote, len(blocks))
							}
						}
					})
				}

				suspendedVMLock.Lock()
				if !suspendedVM && !(devicesLeftToTransferAuthorityFor.Load() >= int32(len(stage5Inputs))) {
					suspendedVMLock.Unlock()

					// We use the background context here instead of the internal context because we want to distinguish
					// between a context cancellation from the outside and getting a response
					cycleThrottleCtx, cancelCycleThrottleCtx := context.WithTimeout(context.Background(), input.migrateToDevice.CycleThrottle)
					defer cancelCycleThrottleCtx()

					select {
					case <-cycleThrottleCtx.Done():
						break

					case <-suspendedVMCh:
						break

					case <-goroutineManager.Context().Done(): // ctx is the goroutineManager.Context() here
						if err := goroutineManager.Context().Err(); err != nil {
							return errors.Join(ErrMounterContextCancelled, err)
						}

						return nil
					}
				} else {
					suspendedVMLock.Unlock()
				}

				totalCycles++
				if len(blocks) < input.migrateToDevice.MaxDirtyBlocks {
					cyclesBelowDirtyBlockTreshold++
					if cyclesBelowDirtyBlockTreshold > input.migrateToDevice.MinCycles {
						markDeviceAsReadyForAuthorityTransfer()
					}
				} else if totalCycles > input.migrateToDevice.MaxCycles {
					markDeviceAsReadyForAuthorityTransfer()
				} else {
					cyclesBelowDirtyBlockTreshold = 0
				}

				if devicesLeftToTransferAuthorityFor.Load() >= int32(len(stage5Inputs)) {
					if err := suspendAndMsyncVM(); err != nil {
						return errors.Join(ErrCouldNotSuspendAndMsyncVM, err)
					}
				}
			}

			if err := to.SendEvent(&packets.Event{
				Type:       packets.EventCustom,
				CustomType: byte(registry.EventCustomTransferAuthority),
			}); err != nil {
				panic(errors.Join(ErrCouldNotSendTransferAuthorityEvent, err))
			}

			if hook := hooks.OnDeviceAuthoritySent; hook != nil {
				hook(uint32(index), input.prev.prev.prev.remote)
			}

			if err := mig.WaitForCompletion(); err != nil {
				return errors.Join(registry.ErrCouldNotWaitForMigrationCompletion, err)
			}

			if err := to.SendEvent(&packets.Event{
				Type: packets.EventCompleted,
			}); err != nil {
				return errors.Join(ErrCouldNotSendCompletedEvent, err)
			}

			if hook := hooks.OnDeviceMigrationCompleted; hook != nil {
				hook(uint32(index), input.prev.prev.prev.remote)
			}

			return nil
		},
	)

	if err != nil {
		panic(errors.Join(ErrCouldNotMigrateToDevice, err))
	}

	for _, deferFuncs := range deferFuncs {
		for _, deferFunc := range deferFuncs {
			defer deferFunc() // We can safely ignore errors here since we never call `addDefer` with a function that could return an error
		}
	}

	if hook := hooks.OnAllMigrationsCompleted; hook != nil {
		hook()
	}

	return
}
