package roles

import (
	"context"
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
	OnBeforeResume func(vmPath string) error
	OnAfterResume  func() error

	OnBeforeSuspend func() error
	OnAfterSuspend  func() error

	OnBeforeStop func() error
	OnAfterStop  func() error

	OnBeforeLeeching func(name string, raddr string, file string, size int64) error
	OnLeechProgress  func(remainingDataSize int64) error
	OnAfterSync      func(name string, delta int) error
	OnAfterSeeding   func(name string, laddr string) error
}

var (
	ErrCouldNotGetDeviceStat = errors.New("could not get device stat")
)

type stage1 struct {
	name  string
	raddr string
	laddr string
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
	mgr      *migration.PathMigrator
	finished chan struct{}
	finalize func() (seed func() (svc *iservices.SeederService, err error), err error)
}

type stage4 struct {
	prev stage3

	seed func() (svc *iservices.SeederService, err error)
}

type stage5 struct {
	prev stage4

	server *grpc.Server
	lis    net.Listener
}

type Peer struct {
	verbose bool

	claimNamespace   func() (string, error)
	releaseNamespace func(namespace string) error

	hypervisorConfiguration config.HypervisorConfiguration
	migratorOptions         *migration.MigratorOptions

	hooks PeerHooks

	cacheBaseDir string

	resumeTimeout   time.Duration
	resumeThreshold int64

	raddrs config.ResourceAddresses
	laddrs config.ResourceAddresses

	agentVSockPort uint32
	stage4Inputs   []stage4

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
	laddrs config.ResourceAddresses,

	resumeTimeout time.Duration,
	resumeThreshold int64,
) *Peer {
	return &Peer{
		verbose: verbose,

		claimNamespace:   claimNamespace,
		releaseNamespace: releaseNamespace,

		hypervisorConfiguration: hypervisorConfiguration,
		migratorOptions:         migratorOptions,

		hooks: hooks,

		cacheBaseDir: cacheBaseDir,

		resumeTimeout:   resumeTimeout,
		resumeThreshold: resumeThreshold,

		raddrs: raddrs,
		laddrs: laddrs,

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

func (p *Peer) Leech(ctx context.Context) error {
	stage1Inputs := []stage1{
		{
			name:  config.InitramfsName,
			raddr: p.raddrs.Initramfs,
			laddr: p.laddrs.Initramfs,
		},
		{
			name:  config.KernelName,
			raddr: p.raddrs.Kernel,
			laddr: p.laddrs.Kernel,
		},
		{
			name:  config.DiskName,
			raddr: p.raddrs.Disk,
			laddr: p.laddrs.Disk,
		},

		{
			name:  config.StateName,
			raddr: p.raddrs.State,
			laddr: p.laddrs.State,
		},
		{
			name:  config.MemoryName,
			raddr: p.raddrs.Memory,
			laddr: p.laddrs.Memory,
		},
	}

	stage2Inputs, stage1Defers, stage1Errs := utils.ConcurrentMap(
		stage1Inputs,
		func(index int, input stage1, output *stage2, addDefer func(deferFunc func() error)) error {
			output.prev = input

			conn, err := grpc.Dial(input.raddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				return err
			}
			addDefer(conn.Close)

			remote, remoteWithMeta := services.NewSeederWithMetaRemoteGrpc(v1.NewSeederWithMetaClient(conn))
			output.remote = remote

			size, port, err := remoteWithMeta.Meta(ctx)
			if err != nil {
				return err
			}
			output.size = size

			if index == 0 {
				p.agentVSockPort = port
			}

			if err := os.MkdirAll(p.cacheBaseDir, os.ModePerm); err != nil {
				return err
			}

			cache, err := os.CreateTemp(p.cacheBaseDir, "*.drft")
			if err != nil {
				return err
			}
			output.cache = cache
			addDefer(cache.Close)
			addDefer(func() error {
				return os.Remove(cache.Name())
			})

			if err := cache.Truncate(size); err != nil {
				return err
			}

			return nil
		},
	)
	p.deferFuncs = append(p.deferFuncs, stage1Defers...)

	for _, err := range stage1Errs {
		if err != nil {
			return err
		}
	}

	netns, err := p.claimNamespace()
	if err != nil {
		return err
	}
	p.deferFuncs = append(p.deferFuncs, []func() error{func() error {
		return p.releaseNamespace(netns)
	}})

	runner := NewRunner(
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
		config.AgentConfiguration{
			AgentVSockPort: p.agentVSockPort,
			ResumeTimeout:  p.resumeTimeout,
		},

		config.StateName,
		config.MemoryName,
	)

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()

		if err := runner.Wait(); err != nil {
			p.errs <- err
		}
	}()

	p.deferFuncs = append(p.deferFuncs, []func() error{runner.Close})
	vmPath, err := runner.Open()
	if err != nil {
		return err
	}

	suspendVM := sync.OnceValue(func() error {
		if hook := p.hooks.OnBeforeSuspend; hook != nil {
			if err := hook(); err != nil {
				return err
			}
		}

		if hook := p.hooks.OnAfterSuspend; hook != nil {
			defer hook()
		}

		return runner.Suspend(ctx)
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

		return runner.Close()
	})

	var nbdDevicesLock sync.Mutex
	mgrs := []*migration.PathMigrator{}
	var mgrsLock sync.Mutex

	continueCh := make(chan struct{})
	closeContinueCh := sync.OnceFunc(func() {
		close(continueCh)
	})

	remainingDataSize := atomic.Int64{}
	for _, stage := range stage2Inputs {
		// Nearest lower multiple of the block size minus one chunk that is never being pulled automatically
		remainingDataSize.Add(((stage.size / client.MaximumBlockSize) * client.MaximumBlockSize) - client.MaximumBlockSize)
	}

	stage3Inputs, stage2Defers, stage2Errs := utils.ConcurrentMap(
		stage2Inputs,
		func(index int, input stage2, output *stage3, addDefer func(deferFunc func() error)) error {
			output.prev = input
			output.finished = make(chan struct{})

			output.local = backend.NewFileBackend(input.cache)
			output.mgr = migration.NewPathMigrator(
				ctx,

				output.local,

				&migration.MigratorOptions{
					Verbose:     p.verbose,
					PullWorkers: p.migratorOptions.PullWorkers,
				},
				&migration.MigratorHooks{
					OnBeforeSync: func() error {
						return suspendVM()
					},
					OnAfterSync: func(dirtyOffsets []int64) error {
						delta := (len(dirtyOffsets) * client.MaximumBlockSize)

						if hook := p.hooks.OnAfterSync; hook != nil {
							if err := hook(input.prev.name, delta); err != nil {
								return err
							}
						}

						return nil
					},

					OnBeforeClose: func() error {
						return stopVM()
					},

					OnChunkIsLocal: func(off int64) error {
						s := remainingDataSize.Add(-client.MaximumBlockSize)

						if hook := p.hooks.OnLeechProgress; hook != nil {
							if err := hook(s); err != nil {
								return err
							}
						}

						if s <= p.resumeThreshold {
							closeContinueCh()
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

				if err := output.mgr.Wait(); err != nil {
					if !utils.IsClosedErr(err) {
						p.errs <- err
					}
				}
			}()

			mgrsLock.Lock()
			mgrs = append(mgrs, output.mgr)
			mgrsLock.Unlock()

			nbdDevicesLock.Lock() // We need to make sure that we call `FindUnusedNBDDevice` synchronously
			defer nbdDevicesLock.Unlock()

			addDefer(output.mgr.Close)
			finalize, file, _, err := output.mgr.Leech(output.prev.remote)
			if err != nil {
				return err
			}
			output.finalize = finalize

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

			if err := unix.Mknod(filepath.Join(vmPath, input.prev.name), unix.S_IFBLK|0666, dev); err != nil {
				return err
			}

			if hook := p.hooks.OnBeforeLeeching; hook != nil {
				if err := hook(input.prev.name, input.prev.raddr, file, input.size); err != nil {
					return err
				}
			}

			return nil
		},
	)
	p.deferFuncs = append(p.deferFuncs, stage2Defers...)

	for _, err := range stage2Errs {
		if err != nil {
			return err
		}
	}

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
	if chosen == 0 {
		// continueCh was selected, continue
	} else {
		// One of the finished channels was selected, return
		return io.EOF
	}

	// Resume as soon as the underlying resources are unlocked
	p.deferFuncs = append(p.deferFuncs, []func() error{runner.Close})
	resumeErrs := make(chan error)
	go func() {
		var err error
		defer func() {
			resumeErrs <- err
		}()

		if hook := p.hooks.OnBeforeResume; hook != nil {
			err = hook(vmPath)
			if err != nil {
				return
			}
		}

		if hook := p.hooks.OnAfterResume; hook != nil {
			defer hook()
		}

		err = runner.Resume(ctx)
	}()

	stage4Inputs, stage3Defers, stage3Errs := utils.ConcurrentMap(
		stage3Inputs,
		func(index int, input stage3, output *stage4, addDefer func(deferFunc func() error)) error {
			output.prev = input

			seed, err := input.finalize()
			if err != nil {
				return err
			}
			output.seed = seed

			return nil
		},
	)
	p.deferFuncs = append(p.deferFuncs, stage3Defers...)

	for _, err := range stage3Errs {
		if err != nil {
			return err
		}
	}

	if err := <-resumeErrs; err != nil {
		return err
	}

	p.stage4Inputs = stage4Inputs

	return nil
}

func (p *Peer) Seed() error {
	stage5Inputs, stage4Defers, stage4Errs := utils.ConcurrentMap(
		p.stage4Inputs,
		func(index int, input stage4, output *stage5, addDefer func(deferFunc func() error)) error {
			output.prev = input

			svc, err := input.seed()
			if err != nil {
				return err
			}

			output.server = grpc.NewServer()

			v1.RegisterSeederWithMetaServer(output.server, services.NewSeederWithMetaServiceGrpc(services.NewSeederWithMetaService(svc, input.prev.local, p.agentVSockPort, p.verbose)))

			output.lis, err = net.Listen("tcp", input.prev.prev.prev.laddr)
			if err != nil {
				return err
			}
			addDefer(output.lis.Close)

			if hook := p.hooks.OnAfterSeeding; hook != nil {
				if err := hook(input.prev.prev.prev.name, input.prev.prev.prev.laddr); err != nil {
					return err
				}
			}

			return nil
		},
	)
	p.deferFuncs = append(p.deferFuncs, stage4Defers...)

	for _, err := range stage4Errs {
		if err != nil {
			return err
		}
	}

	errs := make(chan error)
	for _, stage := range stage5Inputs {
		go func(stage stage5) {
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
	for _, stage := range stage5Inputs {
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

func (p *Peer) Close() error {
	p.closeLock.Lock()
	defer p.closeLock.Unlock()

	func() {
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
