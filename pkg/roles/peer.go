package roles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	iutils "github.com/loopholelabs/drafter/internal/utils"
	"github.com/loopholelabs/drafter/pkg/utils"
	"github.com/loopholelabs/silo/pkg/storage"
	"github.com/loopholelabs/silo/pkg/storage/blocks"
	sconfig "github.com/loopholelabs/silo/pkg/storage/config"
	sdevice "github.com/loopholelabs/silo/pkg/storage/device"
	"github.com/loopholelabs/silo/pkg/storage/dirtytracker"
	"github.com/loopholelabs/silo/pkg/storage/migrator"
	"github.com/loopholelabs/silo/pkg/storage/modules"
	"github.com/loopholelabs/silo/pkg/storage/protocol"
	"github.com/loopholelabs/silo/pkg/storage/protocol/packets"
	"github.com/loopholelabs/silo/pkg/storage/volatilitymonitor"
	"github.com/loopholelabs/silo/pkg/storage/waitingcache"
	"golang.org/x/sys/unix"
)

var (
	ErrCouldNotGetNBDDeviceStat = errors.New("could not get NBD device stat")
)

type MigrateFromDeviceConfiguration struct {
	BasePath,
	OverlayPath,
	StatePath string

	BlockSize uint32
}

type MigrateFromHooks struct {
	OnRemoteDeviceReceived           func(remoteDeviceID uint32, name string)
	OnRemoteDeviceExposed            func(remoteDeviceID uint32, path string)
	OnRemoteDeviceAuthorityReceived  func(remoteDeviceID uint32)
	OnRemoteDeviceMigrationCompleted func(remoteDeviceID uint32)

	OnRemoteAllDevicesReceived     func()
	OnRemoteAllMigrationsCompleted func()

	OnLocalDeviceRequested func(localDeviceID uint32, name string)
	OnLocalDeviceExposed   func(localDeviceID uint32, path string)

	OnLocalAllDevicesRequested func()
}

type MigratedPeer struct {
	Wait  func() error
	Close func() error

	Resume func(
		ctx context.Context,

		resumeTimeout,
		rescueTimeout time.Duration,
	) (
		resumedPeer *ResumedPeer,

		errs error,
	)
}

type ResumedPeer struct {
	Wait  func() error
	Close func() error

	SuspendAndCloseAgentServer func(ctx context.Context, resumeTimeout time.Duration) error

	MakeMigratable func(
		ctx context.Context,

		stateExpiry,
		memoryExpiry,
		initramfsExpiry,
		kernelExpiry,
		diskExpiry,
		configExpiry time.Duration,
	) (migratablePeer *MigratablePeer, errs error)
}

type MigrateToDeviceConfiguration struct {
	MaxDirtyBlocks,
	MinCycles,
	MaxCycles int

	CycleThrottle time.Duration

	Serve bool
}

type MigrateToHooks struct {
	OnBeforeSuspend func()
	OnAfterSuspend  func()

	OnDeviceSent                       func(deviceID uint32, remote bool)
	OnDeviceAuthoritySent              func(deviceID uint32, remote bool)
	OnDeviceInitialMigrationProgress   func(deviceID uint32, remote bool, ready int, total int)
	OnDeviceContinousMigrationProgress func(deviceID uint32, remote bool, delta int)
	OnDeviceFinalMigrationProgress     func(deviceID uint32, remote bool, delta int)
	OnDeviceMigrationCompleted         func(deviceID uint32, remote bool)

	OnAllDevicesSent         func()
	OnAllMigrationsCompleted func()
}

type MigratablePeer struct {
	Close func()

	MigrateTo func(
		ctx context.Context,

		stateDeviceConfiguration,
		memoryDeviceConfiguration,
		initramfsDeviceConfiguration,
		kernelDeviceConfiguration,
		diskDeviceConfiguration,
		configDeviceConfiguration MigrateToDeviceConfiguration,

		suspendTimeout time.Duration,
		concurrency int,

		readers []io.Reader,
		writers []io.Writer,

		hooks MigrateToHooks,
	) (errs error)
}

type peerStage1 struct {
	name string

	base    string
	overlay string
	state   string

	blockSize uint32
}

type peerStage2 struct {
	name string

	blockSize uint32

	id     uint32
	remote bool

	storage storage.StorageProvider
	device  storage.ExposedStorage
}

type peerStage3 struct {
	prev peerStage2

	storage     *modules.Lockable
	orderer     *blocks.PriorityBlockOrder
	totalBlocks int
	dirtyRemote *dirtytracker.DirtyTrackerRemote
}

type Peer struct {
	VMPath string

	Wait  func() error
	Close func() error

	MigrateFrom func(
		ctx context.Context,

		stateDeviceConfiguration,
		memoryDeviceConfiguration,
		initramfsDeviceConfiguration,
		kernelDeviceConfiguration,
		diskDeviceConfiguration,
		configDeviceConfiguration MigrateFromDeviceConfiguration,

		readers []io.Reader,
		writers []io.Writer,

		hooks MigrateFromHooks,
	) (
		migratedPeer *MigratedPeer,

		errs error,
	)
}

func StartPeer(
	hypervisorCtx context.Context,
	rescueCtx context.Context,
	hypervisorConfiguration HypervisorConfiguration,

	stateName string,
	memoryName string,
) (
	peer *Peer,

	errs error,
) {
	peer = &Peer{}

	_, handlePanics, handleGoroutinePanics, cancel, wait, _ := utils.GetPanicHandler(
		hypervisorCtx,
		&errs,
		utils.GetPanicHandlerHooks{},
	)
	defer wait()
	defer cancel()
	defer handlePanics(false)()

	runner, err := StartRunner(
		hypervisorCtx,
		rescueCtx,

		hypervisorConfiguration,

		stateName,
		memoryName,
	)

	// We set both of these even if we return an error since we need to have a way to wait for rescue operations to complete
	peer.Wait = runner.Wait
	peer.Close = func() error {
		if runner.Close != nil {
			if err := runner.Close(); err != nil {
				return err
			}
		}

		if peer.Wait != nil {
			if err := peer.Wait(); err != nil {
				return err
			}
		}

		return nil
	}

	if err != nil {
		panic(err)
	}

	peer.VMPath = runner.VMPath

	// We don't track this because we return the wait function
	handleGoroutinePanics(false, func() {
		if err := runner.Wait(); err != nil {
			panic(err)
		}
	})

	peer.MigrateFrom = func(
		ctx context.Context,

		stateDeviceConfiguration,
		memoryDeviceConfiguration,
		initramfsDeviceConfiguration,
		kernelDeviceConfiguration,
		diskDeviceConfiguration,
		configDeviceConfiguration MigrateFromDeviceConfiguration,

		readers []io.Reader,
		writers []io.Writer,

		hooks MigrateFromHooks,
	) (
		migratedPeer *MigratedPeer,

		errs error,
	) {
		migratedPeer = &MigratedPeer{}

		// We use the background context here instead of the internal context because we want to distinguish
		// between a context cancellation from the outside and getting a response
		allRemoteDevicesReceivedCtx, cancelAllRemoteDevicesReceivedCtx := context.WithCancel(context.Background())
		defer cancelAllRemoteDevicesReceivedCtx()

		allRemoteDevicesReadyCtx, cancelAllRemoteDevicesReadyCtx := context.WithCancel(context.Background())
		defer cancelAllRemoteDevicesReadyCtx()

		// We don't `defer cancelProtocolCtx()` this because we cancel in the wait function
		protocolCtx, cancelProtocolCtx := context.WithCancel(ctx)

		// We overwrite this further down, but this is so that we don't leak the `protocolCtx` if we `panic()` before we set `WaitForMigrationsToComplete`
		migratedPeer.Wait = func() error {
			cancelProtocolCtx()

			return nil
		}

		internalCtx, handlePanics, handleGoroutinePanics, cancel, wait, _ := utils.GetPanicHandler(
			ctx,
			&errs,
			utils.GetPanicHandlerHooks{},
		)
		defer wait()
		defer cancel()
		defer handlePanics(false)()

		// Use an atomic counter and `allDevicesReadyCtx` and instead of a WaitGroup so that we can `select {}` without leaking a goroutine
		var (
			receivedButNotReadyRemoteDevices atomic.Int32

			deviceCloseFuncsLock sync.Mutex
			deviceCloseFuncs     []func() error

			stage2InputsLock sync.Mutex
			stage2Inputs     = []peerStage2{}

			pro *protocol.ProtocolRW
		)
		if len(readers) > 0 && len(writers) > 0 { // Only open the protocol if we want passed in readers and writers
			pro = protocol.NewProtocolRW(
				protocolCtx, // We don't track this because we return the wait function
				readers,
				writers,
				func(p protocol.Protocol, index uint32) {
					var (
						from  *protocol.FromProtocol
						local *waitingcache.WaitingCacheLocal
					)
					from = protocol.NewFromProtocol(
						index,
						func(di *packets.DevInfo) storage.StorageProvider {
							defer handlePanics(false)()

							var (
								base = ""
							)
							switch di.Name {
							case ConfigName:
								base = configDeviceConfiguration.BasePath

							case DiskName:
								base = diskDeviceConfiguration.BasePath

							case InitramfsName:
								base = initramfsDeviceConfiguration.BasePath

							case KernelName:
								base = kernelDeviceConfiguration.BasePath

							case MemoryName:
								base = memoryDeviceConfiguration.BasePath

							case StateName:
								base = stateDeviceConfiguration.BasePath
							}

							if strings.TrimSpace(base) == "" {
								panic(ErrUnknownDeviceName)
							}

							receivedButNotReadyRemoteDevices.Add(1)

							if hook := hooks.OnRemoteDeviceReceived; hook != nil {
								hook(index, di.Name)
							}

							if err := os.MkdirAll(filepath.Dir(base), os.ModePerm); err != nil {
								panic(err)
							}

							src, device, err := sdevice.NewDevice(&sconfig.DeviceSchema{
								Name:      di.Name,
								System:    "file",
								Location:  base,
								Size:      fmt.Sprintf("%v", di.Size),
								BlockSize: fmt.Sprintf("%v", di.Block_size),
								Expose:    true,
							})
							if err != nil {
								panic(err)
							}
							deviceCloseFuncsLock.Lock()
							deviceCloseFuncs = append(deviceCloseFuncs, device.Shutdown) // defer device.Shutdown()
							// We have to close the runner before we close the devices
							deviceCloseFuncs = append(deviceCloseFuncs, runner.Close) // defer runner.Close()
							deviceCloseFuncsLock.Unlock()

							var remote *waitingcache.WaitingCacheRemote
							local, remote = waitingcache.NewWaitingCache(src, int(di.Block_size))
							local.NeedAt = func(offset int64, length int32) {
								// Only access the `from` protocol if it's not already closed
								select {
								case <-protocolCtx.Done():
									return

								default:
								}

								if err := from.NeedAt(offset, length); err != nil {
									panic(err)
								}
							}
							local.DontNeedAt = func(offset int64, length int32) {
								// Only access the `from` protocol if it's not already closed
								select {
								case <-protocolCtx.Done():
									return

								default:
								}

								if err := from.DontNeedAt(offset, length); err != nil {
									panic(err)
								}
							}

							device.SetProvider(local)

							stage2InputsLock.Lock()
							stage2Inputs = append(stage2Inputs, peerStage2{
								name: di.Name,

								blockSize: di.Block_size,

								id:     index,
								remote: true,

								storage: local,
								device:  device,
							})
							stage2InputsLock.Unlock()

							devicePath := filepath.Join("/dev", device.Device())

							deviceInfo, err := os.Stat(devicePath)
							if err != nil {
								panic(err)
							}

							deviceStat, ok := deviceInfo.Sys().(*syscall.Stat_t)
							if !ok {
								panic(ErrCouldNotGetNBDDeviceStat)
							}

							deviceMajor := uint64(deviceStat.Rdev / 256)
							deviceMinor := uint64(deviceStat.Rdev % 256)

							deviceID := int((deviceMajor << 8) | deviceMinor)

							select {
							case <-internalCtx.Done():
								if err := internalCtx.Err(); err != nil {
									panic(internalCtx.Err())
								}

								return nil

							default:
								if err := unix.Mknod(filepath.Join(runner.VMPath, di.Name), unix.S_IFBLK|0666, deviceID); err != nil {
									panic(err)
								}
							}

							if hook := hooks.OnRemoteDeviceExposed; hook != nil {
								hook(index, devicePath)
							}

							return remote
						},
						p,
					)

					handleGoroutinePanics(true, func() {
						if err := from.HandleReadAt(); err != nil {
							panic(err)
						}
					})

					handleGoroutinePanics(true, func() {
						if err := from.HandleWriteAt(); err != nil {
							panic(err)
						}
					})

					handleGoroutinePanics(true, func() {
						if err := from.HandleDevInfo(); err != nil {
							panic(err)
						}
					})

					handleGoroutinePanics(true, func() {
						if err := from.HandleEvent(func(e *packets.Event) {
							switch e.Type {
							case packets.EventCustom:
								switch e.CustomType {
								case byte(EventCustomAllDevicesSent):
									cancelAllRemoteDevicesReceivedCtx()

									if hook := hooks.OnRemoteAllDevicesReceived; hook != nil {
										hook()
									}

								case byte(EventCustomTransferAuthority):
									if receivedButNotReadyRemoteDevices.Add(-1) <= 0 {
										cancelAllRemoteDevicesReadyCtx()
									}

									if hook := hooks.OnRemoteDeviceAuthorityReceived; hook != nil {
										hook(index)
									}
								}

							case packets.EventCompleted:
								if hook := hooks.OnRemoteDeviceMigrationCompleted; hook != nil {
									hook(index)
								}
							}
						}); err != nil {
							panic(err)
						}
					})

					handleGoroutinePanics(true, func() {
						if err := from.HandleDirtyList(func(blocks []uint) {
							if local != nil {
								local.DirtyBlocks(blocks)
							}
						}); err != nil {
							panic(err)
						}
					})
				})
		}

		migratedPeer.Wait = sync.OnceValue(func() error {
			defer cancelProtocolCtx()

			// If we haven't opened the protocol, don't wait for it
			if pro != nil {
				if err := pro.Handle(); err != nil && !errors.Is(err, io.EOF) {
					return err
				}
			}

			// If it hasn't sent any devices, the remote Silo peer doesn't send `EventCustomAllDevicesSent`
			// After the protocol has closed without errors, we can safely assume that we won't receive any
			// additional devices, so we mark all devices as received and ready
			select {
			case <-allRemoteDevicesReceivedCtx.Done():
			default:
				cancelAllRemoteDevicesReceivedCtx()

				// We need to call the hook manually too since we would otherwise only call if we received at least one device
				if hook := hooks.OnRemoteAllDevicesReceived; hook != nil {
					hook()
				}
			}

			cancelAllRemoteDevicesReadyCtx()

			if hook := hooks.OnRemoteAllMigrationsCompleted; hook != nil {
				hook()
			}

			return nil
		})
		migratedPeer.Close = func() (errs error) {
			defer func() {
				if err := migratedPeer.Wait(); err != nil {
					errs = errors.Join(errs, err)
				}
			}()

			deviceCloseFuncsLock.Lock()
			defer deviceCloseFuncsLock.Unlock()

			for _, closeFunc := range deviceCloseFuncs {
				defer func(closeFunc func() error) {
					if err := closeFunc(); err != nil {
						errs = errors.Join(errs, err)
					}
				}(closeFunc)
			}

			return
		}

		// We don't track this because we return the wait function
		handleGoroutinePanics(false, func() {
			if err := migratedPeer.Wait(); err != nil {
				panic(err)
			}
		})

		// We don't track this because we return the close function
		handleGoroutinePanics(false, func() {
			select {
			// Failure case; we cancelled the internal context before all devices are ready
			case <-internalCtx.Done():
				if err := migratedPeer.Close(); err != nil {
					panic(err)
				}

			// Happy case; all devices are ready and we want to wait with closing the devices until we stop the Firecracker process
			case <-allRemoteDevicesReadyCtx.Done():
				<-hypervisorCtx.Done()

				if err := migratedPeer.Close(); err != nil {
					panic(err)
				}

				break
			}
		})

		select {
		case <-internalCtx.Done():
			if err := internalCtx.Err(); err != nil {
				panic(internalCtx.Err())
			}

			return
		case <-allRemoteDevicesReceivedCtx.Done():
			break
		}

		allStage1Inputs := []peerStage1{
			{
				name: StateName,

				base:    stateDeviceConfiguration.BasePath,
				overlay: stateDeviceConfiguration.OverlayPath,
				state:   stateDeviceConfiguration.StatePath,

				blockSize: stateDeviceConfiguration.BlockSize,
			},
			{
				name: MemoryName,

				base:    memoryDeviceConfiguration.BasePath,
				overlay: memoryDeviceConfiguration.OverlayPath,
				state:   memoryDeviceConfiguration.StatePath,

				blockSize: memoryDeviceConfiguration.BlockSize,
			},
			{
				name: InitramfsName,

				base:    initramfsDeviceConfiguration.BasePath,
				overlay: initramfsDeviceConfiguration.OverlayPath,
				state:   initramfsDeviceConfiguration.StatePath,

				blockSize: initramfsDeviceConfiguration.BlockSize,
			},
			{
				name: KernelName,

				base:    kernelDeviceConfiguration.BasePath,
				overlay: kernelDeviceConfiguration.OverlayPath,
				state:   kernelDeviceConfiguration.StatePath,

				blockSize: kernelDeviceConfiguration.BlockSize,
			},
			{
				name: DiskName,

				base:    diskDeviceConfiguration.BasePath,
				overlay: diskDeviceConfiguration.OverlayPath,
				state:   diskDeviceConfiguration.StatePath,

				blockSize: diskDeviceConfiguration.BlockSize,
			},
			{
				name: ConfigName,

				base:    configDeviceConfiguration.BasePath,
				overlay: configDeviceConfiguration.OverlayPath,
				state:   configDeviceConfiguration.StatePath,

				blockSize: configDeviceConfiguration.BlockSize,
			},
		}

		stage1Inputs := []peerStage1{}
		for _, input := range allStage1Inputs {
			if slices.ContainsFunc(
				stage2Inputs,
				func(r peerStage2) bool {
					return input.name == r.name
				},
			) {
				continue
			}

			stage1Inputs = append(stage1Inputs, input)
		}

		// Use an atomic counter instead of a WaitGroup so that we can wait without leaking a goroutine
		var remainingRequestedLocalDevices atomic.Int32
		remainingRequestedLocalDevices.Add(int32(len(stage1Inputs)))

		_, deferFuncs, err := iutils.ConcurrentMap(
			stage1Inputs,
			func(index int, input peerStage1, _ *struct{}, addDefer func(deferFunc func() error)) error {
				if hook := hooks.OnLocalDeviceRequested; hook != nil {
					hook(uint32(index), input.name)
				}

				if remainingRequestedLocalDevices.Add(-1) <= 0 {
					if hook := hooks.OnLocalAllDevicesRequested; hook != nil {
						hook()
					}
				}

				stat, err := os.Stat(input.base)
				if err != nil {
					return err
				}

				var (
					local  storage.StorageProvider
					device storage.ExposedStorage
				)
				if strings.TrimSpace(input.overlay) == "" || strings.TrimSpace(input.state) == "" {
					local, device, err = sdevice.NewDevice(&sconfig.DeviceSchema{
						Name:      input.name,
						System:    "file",
						Location:  input.base,
						Size:      fmt.Sprintf("%v", stat.Size()),
						BlockSize: fmt.Sprintf("%v", input.blockSize),
						Expose:    true,
					})
				} else {
					if err := os.MkdirAll(filepath.Dir(input.overlay), os.ModePerm); err != nil {
						return err
					}

					if err := os.MkdirAll(filepath.Dir(input.state), os.ModePerm); err != nil {
						return err
					}

					local, device, err = sdevice.NewDevice(&sconfig.DeviceSchema{
						Name:      input.name,
						System:    "sparsefile",
						Location:  input.overlay,
						Size:      fmt.Sprintf("%v", stat.Size()),
						BlockSize: fmt.Sprintf("%v", input.blockSize),
						Expose:    true,
						ROSource: &sconfig.DeviceSchema{
							Name:     input.state,
							System:   "file",
							Location: input.base,
							Size:     fmt.Sprintf("%v", stat.Size()),
						},
					})
				}
				if err != nil {
					return err
				}
				addDefer(local.Close)
				addDefer(device.Shutdown)

				device.SetProvider(local)

				stage2InputsLock.Lock()
				stage2Inputs = append(stage2Inputs, peerStage2{
					name: input.name,

					blockSize: input.blockSize,

					id:     uint32(index),
					remote: false,

					storage: local,
					device:  device,
				})
				stage2InputsLock.Unlock()

				devicePath := filepath.Join("/dev", device.Device())

				deviceInfo, err := os.Stat(devicePath)
				if err != nil {
					return err
				}

				deviceStat, ok := deviceInfo.Sys().(*syscall.Stat_t)
				if !ok {
					return ErrCouldNotGetNBDDeviceStat
				}

				deviceMajor := uint64(deviceStat.Rdev / 256)
				deviceMinor := uint64(deviceStat.Rdev % 256)

				deviceID := int((deviceMajor << 8) | deviceMinor)

				select {
				case <-internalCtx.Done():
					if err := internalCtx.Err(); err != nil {
						return internalCtx.Err()
					}

					return nil

				default:
					if err := unix.Mknod(filepath.Join(runner.VMPath, input.name), unix.S_IFBLK|0666, deviceID); err != nil {
						return err
					}
				}

				if hook := hooks.OnLocalDeviceExposed; hook != nil {
					hook(uint32(index), devicePath)
				}

				return nil
			},
		)

		// Make sure that we schedule the `deferFuncs` even if we get an error during device setup
		for _, deferFuncs := range deferFuncs {
			for _, deferFunc := range deferFuncs {
				deviceCloseFuncsLock.Lock()
				deviceCloseFuncs = append(deviceCloseFuncs, deferFunc) // defer deferFunc()
				deviceCloseFuncsLock.Unlock()
			}
		}

		if err != nil {
			panic(err)
		}

		select {
		case <-internalCtx.Done():
			if err := internalCtx.Err(); err != nil {
				panic(internalCtx.Err())
			}

			return
		case <-allRemoteDevicesReadyCtx.Done():
			break
		}

		migratedPeer.Resume = func(
			ctx context.Context,

			resumeTimeout,
			rescueTimeout time.Duration,
		) (resumedPeer *ResumedPeer, errs error) {
			packageConfigFile, err := os.Open(configDeviceConfiguration.BasePath)
			if err != nil {
				return nil, err
			}
			defer packageConfigFile.Close()

			var packageConfig PackageConfiguration
			if err := json.NewDecoder(packageConfigFile).Decode(&packageConfig); err != nil {
				return nil, err
			}

			resumedRunner, err := runner.Resume(ctx, resumeTimeout, rescueTimeout, packageConfig.AgentVSockPort)
			if err != nil {
				return nil, err
			}

			return &ResumedPeer{
				Wait:  resumedRunner.Wait,
				Close: resumedRunner.Close,

				SuspendAndCloseAgentServer: resumedRunner.SuspendAndCloseAgentServer,

				MakeMigratable: func(
					ctx context.Context,

					stateExpiry,
					memoryExpiry,
					initramfsExpiry,
					kernelExpiry,
					diskExpiry,
					configExpiry time.Duration,
				) (migratablePeer *MigratablePeer, errs error) {
					migratablePeer = &MigratablePeer{}

					allStage3Inputs, deferFuncs, err := iutils.ConcurrentMap(
						stage2Inputs,
						func(index int, input peerStage2, output *peerStage3, addDefer func(deferFunc func() error)) error {
							output.prev = input

							var expiry time.Duration
							switch input.name {
							case ConfigName:
								expiry = configExpiry

							case DiskName:
								expiry = diskExpiry

							case InitramfsName:
								expiry = initramfsExpiry

							case KernelName:
								expiry = kernelExpiry

							case MemoryName:
								expiry = memoryExpiry

							case StateName:
								expiry = stateExpiry

								// No need for a default case/check here - we validate that all resources have valid names earlier
							}

							dirtyLocal, dirtyRemote := dirtytracker.NewDirtyTracker(input.storage, int(input.blockSize))
							output.dirtyRemote = dirtyRemote
							monitor := volatilitymonitor.NewVolatilityMonitor(dirtyLocal, int(input.blockSize), expiry)

							local := modules.NewLockable(monitor)
							output.storage = local
							addDefer(func() error {
								local.Unlock()

								return nil
							})

							input.device.SetProvider(local)

							totalBlocks := (int(local.Size()) + int(input.blockSize) - 1) / int(input.blockSize)
							output.totalBlocks = totalBlocks

							orderer := blocks.NewPriorityBlockOrder(totalBlocks, monitor)
							output.orderer = orderer
							orderer.AddAll()

							return nil
						},
					)

					migratablePeer.Close = func() {
						// Make sure that we schedule the `deferFuncs` even if we get an error
						for _, deferFuncs := range deferFuncs {
							for _, deferFunc := range deferFuncs {
								defer deferFunc() // We can safely ignore errors here since we never call `addDefer` with a function that could return an error
							}
						}
					}

					if err != nil {
						// Make sure that we schedule the `deferFuncs` even if we get an error
						migratablePeer.Close()

						panic(err)
					}

					migratablePeer.MigrateTo = func(
						ctx context.Context,

						stateDeviceConfiguration,
						memoryDeviceConfiguration,
						initramfsDeviceConfiguration,
						kernelDeviceConfiguration,
						diskDeviceConfiguration,
						configDeviceConfiguration MigrateToDeviceConfiguration,

						suspendTimeout time.Duration,
						concurrency int,

						readers []io.Reader,
						writers []io.Writer,

						hooks MigrateToHooks,
					) (errs error) {
						ctx, handlePanics, handleGoroutinePanics, cancel, wait, _ := utils.GetPanicHandler(
							ctx,
							&errs,
							utils.GetPanicHandlerHooks{},
						)
						defer wait()
						defer cancel()
						defer handlePanics(false)()

						pro := protocol.NewProtocolRW(
							ctx,
							readers,
							writers,
							nil,
						)

						handleGoroutinePanics(true, func() {
							if err := pro.Handle(); err != nil && !errors.Is(err, io.EOF) {
								panic(err)
							}
						})

						var (
							devicesLeftToSend                 atomic.Int32
							devicesLeftToTransferAuthorityFor atomic.Int32

							suspendedVMLock sync.Mutex
							suspendedVM     bool
						)

						// We use the background context here instead of the internal context because we want to distinguish
						// between a context cancellation from the outside and getting a response
						suspendedVMCtx, cancelSuspendedVMCtx := context.WithCancel(context.Background())
						defer cancelSuspendedVMCtx()

						suspendAndMsyncVM := sync.OnceValue(func() error {
							if hook := hooks.OnBeforeSuspend; hook != nil {
								hook()
							}

							if err := resumedPeer.SuspendAndCloseAgentServer(ctx, suspendTimeout); err != nil {
								return err
							}

							if err := resumedRunner.Msync(ctx); err != nil {
								return err
							}

							if hook := hooks.OnAfterSuspend; hook != nil {
								hook()
							}

							suspendedVMLock.Lock()
							suspendedVM = true
							suspendedVMLock.Unlock()

							cancelSuspendedVMCtx()

							return nil
						})

						stage3Inputs := []peerStage3{}
						for _, input := range allStage3Inputs {
							var serve bool
							switch input.prev.name {
							case ConfigName:
								serve = configDeviceConfiguration.Serve

							case DiskName:
								serve = diskDeviceConfiguration.Serve

							case InitramfsName:
								serve = initramfsDeviceConfiguration.Serve

							case KernelName:
								serve = kernelDeviceConfiguration.Serve

							case MemoryName:
								serve = memoryDeviceConfiguration.Serve

							case StateName:
								serve = stateDeviceConfiguration.Serve

								// No need for a default case/check here - we validate that all resources have valid names earlier
							}

							if !serve {
								continue
							}

							stage3Inputs = append(stage3Inputs, input)
						}

						_, deferFuncs, err := iutils.ConcurrentMap(
							stage3Inputs,
							func(index int, input peerStage3, _ *struct{}, _ func(deferFunc func() error)) error {
								var serve bool
								switch input.prev.name {
								case ConfigName:
									serve = configDeviceConfiguration.Serve

								case DiskName:
									serve = diskDeviceConfiguration.Serve

								case InitramfsName:
									serve = initramfsDeviceConfiguration.Serve

								case KernelName:
									serve = kernelDeviceConfiguration.Serve

								case MemoryName:
									serve = memoryDeviceConfiguration.Serve

								case StateName:
									serve = stateDeviceConfiguration.Serve

									// No need for a default case/check here - we validate that all resources have valid names earlier
								}

								if !serve {
									return nil
								}

								to := protocol.NewToProtocol(input.storage.Size(), uint32(index), pro)

								if err := to.SendDevInfo(input.prev.name, input.prev.blockSize); err != nil {
									return err
								}

								if hook := hooks.OnDeviceSent; hook != nil {
									hook(uint32(index), input.prev.remote)
								}

								devicesLeftToSend.Add(1)
								if devicesLeftToSend.Load() >= int32(len(stage3Inputs)) {
									handleGoroutinePanics(true, func() {
										if err := to.SendEvent(&packets.Event{
											Type:       packets.EventCustom,
											CustomType: byte(EventCustomAllDevicesSent),
										}); err != nil {
											panic(err)
										}

										if hook := hooks.OnAllDevicesSent; hook != nil {
											hook()
										}
									})
								}

								handleGoroutinePanics(true, func() {
									if err := to.HandleNeedAt(func(offset int64, length int32) {
										// Prioritize blocks
										endOffset := uint64(offset + int64(length))
										if endOffset > uint64(input.storage.Size()) {
											endOffset = uint64(input.storage.Size())
										}

										startBlock := int(offset / int64(input.prev.blockSize))
										endBlock := int((endOffset-1)/uint64(input.prev.blockSize)) + 1
										for b := startBlock; b < endBlock; b++ {
											input.orderer.PrioritiseBlock(b)
										}
									}); err != nil {
										panic(err)
									}
								})

								handleGoroutinePanics(true, func() {
									if err := to.HandleDontNeedAt(func(offset int64, length int32) {
										// Deprioritize blocks
										endOffset := uint64(offset + int64(length))
										if endOffset > uint64(input.storage.Size()) {
											endOffset = uint64(input.storage.Size())
										}

										startBlock := int(offset / int64(input.storage.Size()))
										endBlock := int((endOffset-1)/uint64(input.storage.Size())) + 1
										for b := startBlock; b < endBlock; b++ {
											input.orderer.Remove(b)
										}
									}); err != nil {
										panic(err)
									}
								})

								cfg := migrator.NewMigratorConfig().WithBlockSize(int(input.prev.blockSize))
								cfg.Concurrency = map[int]int{
									storage.BlockTypeAny:      concurrency,
									storage.BlockTypeStandard: concurrency,
									storage.BlockTypeDirty:    concurrency,
									storage.BlockTypePriority: concurrency,
								}
								cfg.Progress_handler = func(p *migrator.MigrationProgress) {
									if hook := hooks.OnDeviceInitialMigrationProgress; hook != nil {
										hook(uint32(index), input.prev.remote, p.Ready_blocks, p.Total_blocks)
									}
								}

								mig, err := migrator.NewMigrator(input.dirtyRemote, to, input.orderer, cfg)
								if err != nil {
									return err
								}

								if err := mig.Migrate(input.totalBlocks); err != nil {
									return err
								}

								if err := mig.WaitForCompletion(); err != nil {
									return err
								}

								markDeviceAsReadyForAuthorityTransfer := sync.OnceFunc(func() {
									devicesLeftToTransferAuthorityFor.Add(1)
								})

								var (
									maxDirtyBlocks int
									minCycles      int
									maxCycles      int

									cycleThrottle time.Duration
								)
								switch input.prev.name {
								case ConfigName:
									maxDirtyBlocks = configDeviceConfiguration.MaxDirtyBlocks
									minCycles = configDeviceConfiguration.MinCycles
									maxCycles = configDeviceConfiguration.MaxCycles

									cycleThrottle = configDeviceConfiguration.CycleThrottle

								case DiskName:
									maxDirtyBlocks = diskDeviceConfiguration.MaxDirtyBlocks
									minCycles = diskDeviceConfiguration.MinCycles
									maxCycles = diskDeviceConfiguration.MaxCycles

									cycleThrottle = diskDeviceConfiguration.CycleThrottle

								case InitramfsName:
									maxDirtyBlocks = initramfsDeviceConfiguration.MaxDirtyBlocks
									minCycles = initramfsDeviceConfiguration.MinCycles
									maxCycles = initramfsDeviceConfiguration.MaxCycles

									cycleThrottle = initramfsDeviceConfiguration.CycleThrottle

								case KernelName:
									maxDirtyBlocks = kernelDeviceConfiguration.MaxDirtyBlocks
									minCycles = kernelDeviceConfiguration.MinCycles
									maxCycles = kernelDeviceConfiguration.MaxCycles

									cycleThrottle = kernelDeviceConfiguration.CycleThrottle

								case MemoryName:
									maxDirtyBlocks = memoryDeviceConfiguration.MaxDirtyBlocks
									minCycles = memoryDeviceConfiguration.MinCycles
									maxCycles = memoryDeviceConfiguration.MaxCycles

									cycleThrottle = memoryDeviceConfiguration.CycleThrottle

								case StateName:
									maxDirtyBlocks = stateDeviceConfiguration.MaxDirtyBlocks
									minCycles = stateDeviceConfiguration.MinCycles
									maxCycles = stateDeviceConfiguration.MaxCycles

									cycleThrottle = stateDeviceConfiguration.CycleThrottle

									// No need for a default case/check here - we validate that all resources have valid names earlier
								}

								var (
									cyclesBelowDirtyBlockTreshold = 0
									totalCycles                   = 0
									ongoingMigrationsWg           sync.WaitGroup
								)
								for {
									suspendedVMLock.Lock()
									// We only need to `msync` for the memory because `msync` only affects the memory
									if !suspendedVM && input.prev.name == MemoryName {
										if err := resumedRunner.Msync(ctx); err != nil {
											suspendedVMLock.Unlock()

											return err
										}
									}
									suspendedVMLock.Unlock()

									ongoingMigrationsWg.Wait()

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
										if err := to.DirtyList(blocks); err != nil {
											return err
										}

										ongoingMigrationsWg.Add(1)
										handleGoroutinePanics(true, func() {
											defer ongoingMigrationsWg.Done()

											if err := mig.MigrateDirty(blocks); err != nil {
												panic(err)
											}

											suspendedVMLock.Lock()
											defer suspendedVMLock.Unlock()

											if suspendedVM {
												if hook := hooks.OnDeviceFinalMigrationProgress; hook != nil {
													hook(uint32(index), input.prev.remote, len(blocks))
												}
											} else {
												if hook := hooks.OnDeviceContinousMigrationProgress; hook != nil {
													hook(uint32(index), input.prev.remote, len(blocks))
												}
											}
										})
									}

									suspendedVMLock.Lock()
									if !suspendedVM && !(devicesLeftToTransferAuthorityFor.Load() >= int32(len(stage3Inputs))) {
										suspendedVMLock.Unlock()

										// We use the background context here instead of the internal context because we want to distinguish
										// between a context cancellation from the outside and getting a response
										cycleThrottleCtx, cancelCycleThrottleCtx := context.WithTimeout(context.Background(), cycleThrottle)
										defer cancelCycleThrottleCtx()

										select {
										case <-cycleThrottleCtx.Done():
											break

										case <-suspendedVMCtx.Done():
											break

										case <-ctx.Done(): // ctx is the internalCtx here
											if err := ctx.Err(); err != nil {
												return ctx.Err()
											}

											return nil
										}
									} else {
										suspendedVMLock.Unlock()
									}

									totalCycles++
									if len(blocks) < maxDirtyBlocks {
										cyclesBelowDirtyBlockTreshold++
										if cyclesBelowDirtyBlockTreshold > minCycles {
											markDeviceAsReadyForAuthorityTransfer()
										}
									} else if totalCycles > maxCycles {
										markDeviceAsReadyForAuthorityTransfer()
									} else {
										cyclesBelowDirtyBlockTreshold = 0
									}

									if devicesLeftToTransferAuthorityFor.Load() >= int32(len(stage3Inputs)) {
										if err := suspendAndMsyncVM(); err != nil {
											return err
										}
									}
								}

								if err := to.SendEvent(&packets.Event{
									Type:       packets.EventCustom,
									CustomType: byte(EventCustomTransferAuthority),
								}); err != nil {
									panic(err)
								}

								if hook := hooks.OnDeviceAuthoritySent; hook != nil {
									hook(uint32(index), input.prev.remote)
								}

								if err := mig.WaitForCompletion(); err != nil {
									return err
								}

								if err := to.SendEvent(&packets.Event{
									Type: packets.EventCompleted,
								}); err != nil {
									return err
								}

								if hook := hooks.OnDeviceMigrationCompleted; hook != nil {
									hook(uint32(index), input.prev.remote)
								}

								return nil
							},
						)

						if err != nil {
							panic(err)
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

					return
				},
			}, nil
		}

		return
	}

	return
}
