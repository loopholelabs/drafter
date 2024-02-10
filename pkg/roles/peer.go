package roles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	v1 "github.com/loopholelabs/drafter/pkg/api/proto/migration/v1"
	"github.com/loopholelabs/drafter/pkg/config"
	"github.com/loopholelabs/drafter/pkg/remotes"
	"github.com/loopholelabs/drafter/pkg/services"
	"github.com/loopholelabs/drafter/pkg/utils"
	"github.com/pojntfx/go-nbd/pkg/backend"
	"github.com/pojntfx/go-nbd/pkg/client"
	"github.com/pojntfx/r3map/pkg/migration"
	iservices "github.com/pojntfx/r3map/pkg/services"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type PeerHooks struct {
	OnBeforeSuspend func() error
	OnAfterSuspend  func() error

	OnBeforeStop func() error
	OnAfterStop  func() error

	OnStateLeechProgress     func(remainingDataSize int64) error
	OnMemoryLeechProgress    func(remainingDataSize int64) error
	OnInitramfsLeechProgress func(remainingDataSize int64) error
	OnKernelLeechProgress    func(remainingDataSize int64) error
	OnDiskLeechProgress      func(remainingDataSize int64) error
	OnConfigLeechProgress    func(remainingDataSize int64) error
}

var (
	ErrCouldNotGetDeviceStat         = errors.New("could not get device stat")
	ErrNoRaddrOrPathGivenForResource = errors.New("no raddr or path given for resource")
)

type stage1 struct {
	name            string
	raddr           string
	path            string
	laddr           string
	resumeThreshold int64
	onLeechProgress func(remainingDataSize int64) error
}

type stage2 struct {
	prev stage1

	remote *iservices.SeederRemote
	size   int64
	cache  *os.File
}

type stage3 struct {
	prev stage2

	local    backend.Backend
	finished chan struct{}
	finalize func() (seed func() (svc *iservices.SeederService, err error), err error)
	delta    int
}

type stage4 struct {
	prev stage3

	seed func() (svc *iservices.SeederService, err error)
}

type stage5 struct {
	prev stage4

	server *grpc.Server
	lis    net.Listener
	raddr  string
}

type Sizes struct {
	State     int64
	Memory    int64
	Initramfs int64
	Kernel    int64
	Disk      int64
	Config    int64
}

type Deltas struct {
	State     int
	Memory    int
	Initramfs int
	Kernel    int
	Disk      int
	Config    int
}

type Peer struct {
	verbose bool

	claimNamespace   func() (string, error)
	releaseNamespace func(namespace string) error

	hypervisorConfiguration config.HypervisorConfiguration
	migratorOptions         *migration.MigratorOptions

	hooks PeerHooks

	cacheBaseDir string

	resumeTimeout time.Duration

	raddrs           config.ResourceAddresses
	paths            config.ResourceAddresses
	laddrs           config.ResourceAddresses
	resumeThresholds config.ResourceResumeThresholds

	stage2Inputs []stage2
	stage3Inputs []stage3
	runner       *Runner
	vmPath       string
	suspendVM    func() error
	stage4Inputs []stage4
	stage5Inputs []stage5

	deferFuncs [][]func() error

	wg sync.WaitGroup

	closeLock sync.Mutex

	errs chan error
}

func NewPeer(
	verbose bool,

	claimNamespace func() (string, error),
	releaseNamespace func(namespace string) error,

	hypervisorConfiguration config.HypervisorConfiguration,
	migratorOptions *migration.MigratorOptions,

	hooks PeerHooks,

	cacheBaseDir string,

	raddrs config.ResourceAddresses,
	paths config.ResourceAddresses,
	laddrs config.ResourceAddresses,
	resumeThresholds config.ResourceResumeThresholds,

	resumeTimeout time.Duration,
) *Peer {
	return &Peer{
		verbose: verbose,

		claimNamespace:   claimNamespace,
		releaseNamespace: releaseNamespace,

		hypervisorConfiguration: hypervisorConfiguration,
		migratorOptions:         migratorOptions,

		hooks: hooks,

		cacheBaseDir: cacheBaseDir,

		resumeTimeout: resumeTimeout,

		raddrs:           raddrs,
		paths:            paths,
		laddrs:           laddrs,
		resumeThresholds: resumeThresholds,

		deferFuncs: [][]func() error{},

		wg:   sync.WaitGroup{},
		errs: make(chan error),
	}
}

func (p *Peer) Wait() error {
	for err := range p.errs {
		if err != nil {
			return err
		}
	}

	p.wg.Wait()

	return nil
}

func (p *Peer) Connect(ctx context.Context) (*Sizes, error) {
	stage1Inputs := []stage1{
		{
			name:            config.InitramfsName,
			raddr:           p.raddrs.Initramfs,
			path:            p.paths.Initramfs,
			laddr:           p.laddrs.Initramfs,
			resumeThreshold: p.resumeThresholds.Initramfs,
			onLeechProgress: p.hooks.OnInitramfsLeechProgress,
		},
		{
			name:            config.KernelName,
			raddr:           p.raddrs.Kernel,
			path:            p.paths.Kernel,
			laddr:           p.laddrs.Kernel,
			resumeThreshold: p.resumeThresholds.Kernel,
			onLeechProgress: p.hooks.OnKernelLeechProgress,
		},
		{
			name:            config.DiskName,
			raddr:           p.raddrs.Disk,
			path:            p.paths.Disk,
			laddr:           p.laddrs.Disk,
			resumeThreshold: p.resumeThresholds.Disk,
			onLeechProgress: p.hooks.OnDiskLeechProgress,
		},

		{
			name:            config.StateName,
			raddr:           p.raddrs.State,
			path:            p.paths.State,
			laddr:           p.laddrs.State,
			resumeThreshold: p.resumeThresholds.State,
			onLeechProgress: p.hooks.OnStateLeechProgress,
		},
		{
			name:            config.MemoryName,
			raddr:           p.raddrs.Memory,
			path:            p.paths.Memory,
			laddr:           p.laddrs.Memory,
			resumeThreshold: p.resumeThresholds.Memory,
			onLeechProgress: p.hooks.OnMemoryLeechProgress,
		},

		{
			name:            config.ConfigName,
			raddr:           p.raddrs.Config,
			path:            p.paths.Config,
			laddr:           p.laddrs.Config,
			resumeThreshold: p.resumeThresholds.Config,
			onLeechProgress: p.hooks.OnConfigLeechProgress,
		},
	}

	for _, stage := range stage1Inputs {
		if stage.raddr == "" && stage.path == "" {
			return nil, ErrNoRaddrOrPathGivenForResource
		}
	}

	stage2Inputs, stage1Defers, stage1Errs := utils.ConcurrentMap(
		stage1Inputs,
		func(index int, input stage1, output *stage2, addDefer func(deferFunc func() error)) error {
			output.prev = input

			if input.raddr == "" {
				resourceInfo, err := os.Stat(input.path)
				if err != nil {
					return err
				}

				addDefer(func() error {
					if paddingLength := utils.GetBlockDevicePadding(resourceInfo.Size()); paddingLength > 0 {
						resourceFile, err := os.OpenFile(input.path, os.O_WRONLY|os.O_APPEND, os.ModePerm)
						if err != nil {
							return err
						}
						defer resourceFile.Close()

						if _, err := resourceFile.Write(make([]byte, paddingLength)); err != nil {
							return err
						}
					}

					return nil
				})

				output.size = resourceInfo.Size()

				if input.laddr != "" {
					cache, err := os.OpenFile(input.path, os.O_RDWR, os.ModePerm)
					if err != nil {
						return err
					}

					output.cache = cache
					addDefer(cache.Close)
				}

				return nil
			}

			conn, err := grpc.Dial(input.raddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				return err
			}
			addDefer(conn.Close)

			rawRemote, rawRemoteWithMeta := services.NewSeederWithMetaRemoteGrpc(v1.NewSeederWithMetaClient(conn))

			rawSize, err := rawRemoteWithMeta.Meta(ctx)
			if err != nil {
				return err
			}

			// Make sure that we always have at least two blocks in length
			alignedSize := ((rawSize + (client.MaximumBlockSize * 2) - 1) / (client.MaximumBlockSize * 2)) * client.MaximumBlockSize * 2
			output.size = alignedSize

			output.remote = remotes.NewSeederWithAlignedReadsRemote(rawRemote, rawSize, alignedSize)

			if err := os.MkdirAll(p.cacheBaseDir, os.ModePerm); err != nil {
				return err
			}

			var cache *os.File
			if input.path == "" {
				cache, err = os.CreateTemp(p.cacheBaseDir, "*.drft")
				if err != nil {
					return err
				}
				addDefer(func() error {
					return os.Remove(cache.Name())
				})
			} else {
				// Wite changes to specified path instead of cache directory for persistence
				cache, err = os.OpenFile(input.path, os.O_CREATE|os.O_RDWR, os.ModePerm)
				if err != nil {
					return err
				}
			}

			output.cache = cache
			addDefer(cache.Close)

			if err := cache.Truncate(output.size); err != nil {
				return err
			}

			return nil
		},
	)
	p.deferFuncs = append(p.deferFuncs, stage1Defers...)
	p.stage2Inputs = stage2Inputs

	for _, err := range stage1Errs {
		if err != nil {
			return nil, err
		}
	}

	sizes := &Sizes{}

	for _, stage := range p.stage2Inputs {
		switch stage.prev.name {
		case config.InitramfsName:
			sizes.Initramfs = stage.size
		case config.KernelName:
			sizes.Kernel = stage.size
		case config.DiskName:
			sizes.Disk = stage.size

		case config.StateName:
			sizes.State = stage.size
		case config.MemoryName:

			sizes.Memory = stage.size
		case config.ConfigName:
			sizes.Config = stage.size
		}
	}

	return sizes, nil
}

func (p *Peer) Leech(ctx context.Context) (*Deltas, error) {
	netns, err := p.claimNamespace()
	if err != nil {
		return nil, err
	}
	p.deferFuncs = append(p.deferFuncs, []func() error{func() error {
		return p.releaseNamespace(netns)
	}})

	p.runner = NewRunner(
		config.HypervisorConfiguration{
			FirecrackerBin: p.hypervisorConfiguration.FirecrackerBin,
			JailerBin:      p.hypervisorConfiguration.JailerBin,

			ChrootBaseDir: p.hypervisorConfiguration.ChrootBaseDir,

			UID: p.hypervisorConfiguration.UID,
			GID: p.hypervisorConfiguration.GID,

			NetNS:         netns,
			NumaNode:      p.hypervisorConfiguration.NumaNode,
			CgroupVersion: p.hypervisorConfiguration.CgroupVersion,

			EnableOutput: p.hypervisorConfiguration.EnableOutput,
			EnableInput:  p.hypervisorConfiguration.EnableInput,
		},

		config.StateName,
		config.MemoryName,
	)

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()

		if err := p.runner.Wait(); err != nil {
			p.errs <- err
		}
	}()

	p.deferFuncs = append(p.deferFuncs, []func() error{p.runner.Close})
	p.vmPath, err = p.runner.Open()
	if err != nil {
		return nil, err
	}

	p.suspendVM = sync.OnceValue(func() error {
		if hook := p.hooks.OnBeforeSuspend; hook != nil {
			if err := hook(); err != nil {
				return err
			}
		}

		if hook := p.hooks.OnAfterSuspend; hook != nil {
			defer hook()
		}

		return p.runner.Suspend(ctx, p.resumeTimeout)
	})

	stopVM := sync.OnceValue(func() error {
		if hook := p.hooks.OnBeforeStop; hook != nil {
			if err := hook(); err != nil {
				return err
			}
		}

		if hook := p.hooks.OnAfterStop; hook != nil {
			defer hook()
		}

		return p.runner.Close()
	})

	var (
		deviceLookupLock sync.Mutex
		continueWg       sync.WaitGroup
	)
	stage3Inputs, stage2Defers, stage2Errs := utils.ConcurrentMap(
		p.stage2Inputs,
		func(index int, input stage2, output *stage3, addDefer func(deferFunc func() error)) error {
			output.prev = input
			output.finished = make(chan struct{})

			var file string
			if input.prev.raddr == "" {
				if input.prev.laddr == "" {
					mnt := utils.NewLoopMount(input.prev.path)

					deviceLookupLock.Lock() // We need to make sure that we call `GetFree` synchronously
					defer deviceLookupLock.Unlock()

					addDefer(func() error {
						close(output.finished)

						return nil
					})
					addDefer(mnt.Close)
					dev, err := mnt.Open()
					if err != nil {
						return err
					}
					file = dev

					addDefer(stopVM)

					if hook := input.prev.onLeechProgress; hook != nil {
						if err := hook(0); err != nil {
							return err
						}
					}
				} else {
					output.local = backend.NewFileBackend(input.cache)
					sdr := migration.NewPathSeeder(
						output.local,

						&migration.SeederOptions{
							Verbose: p.verbose,
						},
						&migration.SeederHooks{
							OnBeforeSync: func() error {
								return p.suspendVM()
							},

							OnBeforeClose: func() error {
								return stopVM()
							},
						},

						nil,
						nil,
					)

					p.wg.Add(1)
					go func() {
						defer p.wg.Done()
						defer close(output.finished)

						if err := sdr.Wait(); err != nil {
							if !utils.IsClosedErr(err) {
								p.errs <- err
							}
						}
					}()

					deviceLookupLock.Lock() // We need to make sure that we call `GetFree` synchronously
					defer deviceLookupLock.Unlock()

					addDefer(sdr.Close)
					dev, _, svc, err := sdr.Open()
					if err != nil {
						return err
					}
					file = dev
					output.finalize = func() (seed func() (*iservices.SeederService, error), err error) {
						return func() (*iservices.SeederService, error) {
							return svc, nil
						}, nil
					}

					if hook := input.prev.onLeechProgress; hook != nil {
						if err := hook(0); err != nil {
							return err
						}
					}
				}
			} else {
				continueWg.Add(1)
				markStageAsReady := sync.OnceFunc(func() {
					continueWg.Done()
				})

				remainingDataSize := atomic.Int64{}
				// Nearest lower multiple of the block size minus one chunk that is never being pulled automatically
				remainingDataSize.Add(((input.size / client.MaximumBlockSize) * client.MaximumBlockSize) - client.MaximumBlockSize)

				output.local = backend.NewFileBackend(input.cache)
				mgr := migration.NewPathMigrator(
					ctx,

					output.local,

					&migration.MigratorOptions{
						Verbose:     p.verbose,
						PullWorkers: p.migratorOptions.PullWorkers,
					},
					&migration.MigratorHooks{
						OnBeforeSync: func() error {
							return p.suspendVM()
						},
						OnAfterSync: func(dirtyOffsets []int64) error {
							output.delta = (len(dirtyOffsets) * client.MaximumBlockSize)

							return nil
						},

						OnBeforeClose: func() error {
							return stopVM()
						},

						OnChunkIsLocal: func(off int64) error {
							s := remainingDataSize.Add(-client.MaximumBlockSize)

							if hook := input.prev.onLeechProgress; hook != nil {
								if err := hook(s); err != nil {
									return err
								}
							}

							if s <= input.prev.resumeThreshold {
								markStageAsReady()
							}

							return nil
						},
					},

					nil,
					nil,
				)

				p.wg.Add(1)
				go func() {
					defer p.wg.Done()
					defer close(output.finished)
					defer markStageAsReady()

					if err := mgr.Wait(); err != nil {
						if !utils.IsClosedErr(err) {
							p.errs <- err
						}
					}
				}()

				deviceLookupLock.Lock() // We need to make sure that we call `FindUnusedNBDDevice` synchronously
				defer deviceLookupLock.Unlock()

				addDefer(mgr.Close)
				finalize, dev, _, err := mgr.Leech(output.prev.remote)
				if err != nil {
					return err
				}
				file = dev
				output.finalize = finalize
			}

			info, err := os.Stat(file)
			if err != nil {
				return err
			}

			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				return ErrCouldNotGetDeviceStat
			}

			major := uint64(stat.Rdev / 256)
			minor := uint64(stat.Rdev % 256)

			dev := int((major << 8) | minor)

			if err := unix.Mknod(filepath.Join(p.vmPath, input.prev.name), unix.S_IFBLK|0666, dev); err != nil {
				return err
			}

			return nil
		},
	)
	p.deferFuncs = append(p.deferFuncs, stage2Defers...)
	p.stage3Inputs = stage3Inputs

	for _, err := range stage2Errs {
		if err != nil {
			return nil, err
		}
	}

	continueCh := make(chan struct{})
	go func() {
		continueWg.Wait() // This will always eventually return since markStageAsReady() is deferred in the `Wait()` goroutine

		close(continueCh)
	}()

	cases := []reflect.SelectCase{
		{
			Dir:  reflect.SelectRecv,
			Chan: reflect.ValueOf(continueCh),
		},
	}
	for _, stage := range stage3Inputs {
		cases = append(cases, reflect.SelectCase{
			Dir:  reflect.SelectRecv,
			Chan: reflect.ValueOf(stage.finished),
		})
	}

	chosen, _, _ := reflect.Select(cases)
	if chosen != 0 {
		// One of the finished channels was selected, return
		return nil, io.EOF
	}

	// continueCh was selected, continue
	deltas := &Deltas{}

	for _, stage := range p.stage3Inputs {
		switch stage.prev.prev.name {
		case config.InitramfsName:
			deltas.Initramfs = stage.delta
		case config.KernelName:
			deltas.Kernel = stage.delta
		case config.DiskName:
			deltas.Disk = stage.delta

		case config.StateName:
			deltas.State = stage.delta
		case config.MemoryName:
			deltas.Memory = stage.delta

		case config.ConfigName:
			deltas.Config = stage.delta
		}
	}

	return deltas, nil
}

func (p *Peer) Resume(ctx context.Context) (string, error) {
	// Resume as soon as the underlying resources are unlocked
	resumeErrs := make(chan error)
	go func() {
		var err error
		defer func() {
			resumeErrs <- err
		}()

		configPath := ""
		for _, stage := range p.stage3Inputs {
			if stage.prev.prev.name == config.ConfigName {
				configPath = filepath.Join(p.vmPath, stage.prev.prev.name)
				if configPath == "" {
					configPath = stage.prev.prev.path
				}

				break
			}
		}

		configFile, e := os.Open(configPath)
		if e != nil {
			err = e

			return
		}
		defer configFile.Close()

		var packageConfig config.PackageConfiguration
		if e := json.NewDecoder(configFile).Decode(&packageConfig); e != nil {
			err = e

			return
		}

		err = p.runner.Resume(ctx, p.resumeTimeout, packageConfig.AgentVSockPort)
	}()

	stage4Inputs, stage3Defers, stage3Errs := utils.ConcurrentMap(
		p.stage3Inputs,
		func(index int, input stage3, output *stage4, addDefer func(deferFunc func() error)) error {
			output.prev = input

			if input.prev.prev.raddr == "" && input.prev.prev.laddr == "" {
				return nil
			}

			seed, err := input.finalize()
			if err != nil {
				return err
			}
			output.seed = seed

			return nil
		},
	)
	p.deferFuncs = append(p.deferFuncs, stage3Defers...)
	p.stage4Inputs = stage4Inputs

	for _, err := range stage3Errs {
		if err != nil {
			return "", err
		}
	}

	if err := <-resumeErrs; err != nil {
		return "", err
	}

	return p.vmPath, nil
}

func (p *Peer) Seed() (*config.ResourceAddresses, error) {
	stage5Inputs, stage4Defers, stage4Errs := utils.ConcurrentMap(
		p.stage4Inputs,
		func(index int, input stage4, output *stage5, addDefer func(deferFunc func() error)) error {
			output.prev = input

			if input.prev.prev.prev.raddr == "" && input.prev.prev.prev.laddr == "" {
				return nil
			}

			svc, err := input.seed()
			if err != nil {
				return err
			}

			output.server = grpc.NewServer()

			v1.RegisterSeederWithMetaServer(output.server, services.NewSeederWithMetaServiceGrpc(services.NewSeederWithMetaService(svc, input.prev.local, p.verbose)))

			output.lis, err = net.Listen("tcp", input.prev.prev.prev.laddr)
			if err != nil {
				return err
			}
			addDefer(output.lis.Close)
			output.raddr = output.lis.Addr().String()

			return nil
		},
	)
	p.deferFuncs = append(p.deferFuncs, stage4Defers...)
	p.stage5Inputs = stage5Inputs

	for _, err := range stage4Errs {
		if err != nil {
			return nil, err
		}
	}

	laddrs := &config.ResourceAddresses{}

	for _, stage := range p.stage5Inputs {
		switch stage.prev.prev.prev.prev.name {
		case config.InitramfsName:
			laddrs.Initramfs = stage.raddr
		case config.KernelName:
			laddrs.Kernel = stage.raddr
		case config.DiskName:
			laddrs.Disk = stage.raddr

		case config.StateName:
			laddrs.State = stage.raddr
		case config.MemoryName:
			laddrs.Memory = stage.raddr

		case config.ConfigName:
			laddrs.Config = stage.raddr
		}
	}

	return laddrs, nil
}

func (p *Peer) Serve() error {
	errs := make(chan error)
	for _, stage := range p.stage5Inputs {
		go func(stage stage5) {
			if stage.prev.prev.prev.prev.raddr == "" && stage.prev.prev.prev.prev.laddr == "" {
				return
			}

			defer stage.lis.Close()

			if err := stage.server.Serve(stage.lis); err != nil {
				if !utils.IsClosedErr(err) {
					errs <- err
				}

				return
			}
		}(stage)
	}

	cases := []reflect.SelectCase{
		{
			Dir:  reflect.SelectRecv,
			Chan: reflect.ValueOf(errs),
		},
	}
	for _, stage := range p.stage5Inputs {
		cases = append(cases, reflect.SelectCase{
			Dir:  reflect.SelectRecv,
			Chan: reflect.ValueOf(stage.prev.prev.finished),
		})
	}

	chosen, recv, _ := reflect.Select(cases)
	if chosen == 0 {
		// errs was selected
		return fmt.Errorf("%v", recv)
	} else {
		// One of the finished channels was selected, return
		return io.EOF
	}
}

func (p *Peer) Close(suspend bool) error {
	p.closeLock.Lock()
	defer p.closeLock.Unlock()

	if hook := p.suspendVM; suspend && hook != nil {
		if err := hook(); err != nil {
			return err
		}
	}

	func() {
		defer func() {
			p.deferFuncs = [][]func() error{} // Make sure that we only run the defers once
		}()

		for _, deferFuncs := range p.deferFuncs {
			for _, deferFunc := range deferFuncs {
				defer deferFunc()
			}
		}
	}()

	p.wg.Wait()

	if p.errs != nil {
		close(p.errs)

		p.errs = nil
	}

	return nil
}
