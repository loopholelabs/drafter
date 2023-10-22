package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	v1 "github.com/loopholelabs/architekt/pkg/api/proto/migration/v1"
	"github.com/loopholelabs/architekt/pkg/roles"
	"github.com/loopholelabs/architekt/pkg/utils"
	"github.com/pojntfx/go-nbd/pkg/backend"
	"github.com/pojntfx/go-nbd/pkg/client"
	"github.com/pojntfx/r3map/pkg/migration"
	"github.com/schollz/progressbar/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var (
	ErrCouldNotWaitForRunner  = errors.New("could not wait for runner")
	ErrCouldNotWaitForManager = errors.New("could not wait for manager")
	ErrCouldNotFindInstance   = errors.New("could not find instance")
	ErrCouldNotServeSeeder    = errors.New("could not serve seeder")
)

type HypervisorConfiguration struct {
	FirecrackerBin string
	JailerBin      string

	ChrootBaseDir string

	UID int
	GID int

	NumaNode      int
	CgroupVersion int

	EnableOutput bool
	EnableInput  bool
}

type instance struct {
	closeLock        sync.Mutex
	netns            string
	releaseNamespace func(namespace string) error
	conn             *grpc.ClientConn
	runner           *roles.Runner
	cacheFile        *os.File
	mgr              *migration.PathMigrator
	lis              net.Listener
	server           *grpc.Server
	runnerWg         *sync.WaitGroup
	mgrWg            *sync.WaitGroup
	serverWg         *sync.WaitGroup
}

func (i *instance) close() error {
	i.closeLock.Lock()
	defer i.closeLock.Unlock()

	if i.runner != nil {
		_ = i.runner.Close()

		i.runner = nil
	}

	if i.runnerWg != nil {
		i.runnerWg.Wait()
	}

	if i.mgr != nil {
		_ = i.mgr.Close()

		i.mgr = nil
	}

	if i.mgrWg != nil {
		i.mgrWg.Wait()
	}

	if i.cacheFile != nil {
		_ = i.cacheFile.Close()

		_ = os.Remove(i.cacheFile.Name())

		i.cacheFile = nil
	}

	if i.server != nil {
		i.server.GracefulStop()

		i.server = nil
	}

	if i.serverWg != nil {
		i.serverWg.Wait()
	}

	if i.lis != nil {
		_ = i.lis.Close()

		i.lis = nil
	}

	if i.conn != nil {
		_ = i.conn.Close()

		i.conn = nil
	}

	if i.netns != "" && i.releaseNamespace != nil {
		_ = i.releaseNamespace(i.netns)

		i.netns = ""
		i.releaseNamespace = nil
	}

	return nil
}

type WorkerRemote struct {
	ListInstances  func(ctx context.Context) ([]string, error)
	CreateInstance func(ctx context.Context, packageRaddr string) (string, error)
	DeleteInstance func(ctx context.Context, packageRaddr string) error
}

type Worker struct {
	verbose bool

	claimNamespace   func() (string, error)
	releaseNamespace func(namespace string) error

	hypervisorConfiguration HypervisorConfiguration

	lhost string
	ahost string

	instances     map[string]*instance
	instancesLock sync.Mutex
}

func NewWorker(
	verbose bool,

	claimNamespace func() (string, error),
	releaseNamespace func(namespace string) error,

	hypervisorConfiguration HypervisorConfiguration,

	lhost string,
	ahost string,
) *Worker {
	return &Worker{
		verbose: verbose,

		claimNamespace:   claimNamespace,
		releaseNamespace: releaseNamespace,

		hypervisorConfiguration: hypervisorConfiguration,

		lhost: lhost,
		ahost: ahost,

		instances: map[string]*instance{},
	}
}

func (w *Worker) ListInstances(ctx context.Context) ([]string, error) {
	if w.verbose {
		log.Println("ListInstances()")
	}

	packageRaddrs := []string{}

	w.instancesLock.Lock()
	defer w.instancesLock.Unlock()

	for packageRaddr := range w.instances {
		packageRaddrs = append(packageRaddrs, packageRaddr)
	}

	return packageRaddrs, nil
}

func (w *Worker) CreateInstance(ctx context.Context, packageRaddr string) (outputRaddr string, err error) {
	if w.verbose {
		log.Printf("CreateInstance(packageRaddr = %v)", packageRaddr)
	}

	i := &instance{}

	defer func() {
		if re := recover(); re != nil {
			e, ok := re.(error)
			if !ok {
				e = fmt.Errorf("%v", re)
			}

			err = e

			if outputRaddr != "" {
				w.instancesLock.Lock()
				delete(w.instances, outputRaddr)
				w.instancesLock.Unlock()
			}

			_ = i.close()
		}
	}()

	netns, err := w.claimNamespace()
	if err != nil {
		panic(err)
	}

	i.netns = netns
	i.releaseNamespace = w.releaseNamespace

	conn, err := grpc.Dial(packageRaddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		panic(err)
	}

	i.conn = conn

	remote, remoteWithMeta := NewSeederWithMetaRemoteGrpc(v1.NewSeederWithMetaClient(conn))

	size, agentVSockPort, err := remoteWithMeta.Meta(ctx)
	if err != nil {
		panic(err)
	}

	runner := roles.NewRunner(
		utils.HypervisorConfiguration{
			FirecrackerBin: w.hypervisorConfiguration.FirecrackerBin,
			JailerBin:      w.hypervisorConfiguration.JailerBin,

			ChrootBaseDir: w.hypervisorConfiguration.ChrootBaseDir,

			UID: w.hypervisorConfiguration.UID,
			GID: w.hypervisorConfiguration.GID,

			NetNS:         netns,
			NumaNode:      w.hypervisorConfiguration.NumaNode,
			CgroupVersion: w.hypervisorConfiguration.CgroupVersion,

			EnableOutput: w.hypervisorConfiguration.EnableOutput,
			EnableInput:  w.hypervisorConfiguration.EnableInput,
		},
		utils.AgentConfiguration{
			AgentVSockPort: agentVSockPort,
		},
	)

	var runnerWg sync.WaitGroup
	runnerWg.Add(1)
	go func() {
		if err := runner.Wait(); err != nil {
			log.Printf("%v: %v", ErrCouldNotWaitForRunner, err)
		}

		runnerWg.Done()

		if outputRaddr != "" {
			w.instancesLock.Lock()
			delete(w.instances, outputRaddr)
			w.instancesLock.Unlock()
		}

		_ = i.close()
	}()

	i.runner = runner
	i.runnerWg = &runnerWg

	f, err := os.CreateTemp("", "")
	if err != nil {
		panic(err)
	}

	i.cacheFile = f

	if err := f.Truncate(size); err != nil {
		panic(err)
	}

	var (
		bar           *progressbar.ProgressBar
		pulledOffsets = atomic.Int64{}
	)
	if w.verbose {
		bar = progressbar.NewOptions64(
			size,
			progressbar.OptionSetDescription("Pulling"),
			progressbar.OptionShowBytes(true),
			progressbar.OptionOnCompletion(func() {
				fmt.Fprint(os.Stderr, "\n")
			}),
			progressbar.OptionSetWriter(os.Stderr),
			progressbar.OptionThrottle(100*time.Millisecond),
			progressbar.OptionShowCount(),
			progressbar.OptionFullWidth(),
			// VT-100 compatibility
			progressbar.OptionUseANSICodes(true),
			progressbar.OptionSetTheme(progressbar.Theme{
				Saucer:        "=",
				SaucerHead:    ">",
				SaucerPadding: " ",
				BarStart:      "[",
				BarEnd:        "]",
			}),
		)

		bar.Add(client.MaximumBlockSize)
	}

	continueCh := make(chan struct{})

	b := backend.NewFileBackend(f)
	mgr := migration.NewPathMigrator(
		ctx,

		b,

		&migration.MigratorOptions{
			Verbose: false,
		},
		&migration.MigratorHooks{
			OnBeforeSync: func() error {
				if w.verbose {
					before := time.Now()
					defer func() {
						log.Println("Suspend:", time.Since(before))
					}()

					log.Println("Suspending VM")
				}

				return runner.Suspend(ctx)
			},
			OnAfterSync: func(dirtyOffsets []int64) error {
				if w.verbose {
					bar.Clear()

					delta := (len(dirtyOffsets) * client.MaximumBlockSize)

					log.Printf("Invalidated: %.2f MB (%.2f Mb)", float64(delta)/(1024*1024), (float64(delta)/(1024*1024))*8)

					bar.ChangeMax64(size + int64(delta))

					bar.Describe("Finalizing")
				}

				return nil
			},

			OnBeforeClose: func() error {
				if w.verbose {
					log.Println("Stopping VM")
				}

				return runner.Close()
			},

			OnChunkIsLocal: func(off int64) error {
				if w.verbose {
					bar.Add(client.MaximumBlockSize)
				}

				pulledOffsets.Add(client.MaximumBlockSize)

				if pulledOffsets.Load()+client.MaximumBlockSize >= size && continueCh != nil {
					close(continueCh)

					continueCh = nil
				}

				return nil
			},
		},

		nil,
		nil,
	)

	cancelled := make(chan struct{})
	var mgrWg sync.WaitGroup
	mgrWg.Add(1)
	go func() {
		if err := mgr.Wait(); err != nil {
			log.Printf("%v: %v", ErrCouldNotWaitForManager, err)
		}

		mgrWg.Done()

		if outputRaddr != "" {
			w.instancesLock.Lock()
			delete(w.instances, outputRaddr)
			w.instancesLock.Unlock()
		}

		_ = i.close()

		close(cancelled)
	}()

	if w.verbose {
		log.Println("Leeching from", packageRaddr)
	}

	i.mgr = mgr
	i.mgrWg = &mgrWg

	finalize, _, err := mgr.Leech(remote)
	if err != nil {
		panic(err)
	}

	select {
	case <-cancelled:
		panic(ErrCouldNotWaitForManager)

	case <-continueCh:
	}

	before := time.Now()

	seed, file, err := finalize()
	if err != nil {
		panic(err)
	}

	if w.verbose {
		bar.Clear()
	}

	if w.verbose {
		log.Println("Resuming VM on", file)
	}

	if err := runner.Resume(ctx, file); err != nil {
		panic(err)
	}

	if w.verbose {
		log.Println("Resume:", time.Since(before))
	}

	svc, err := seed()
	if err != nil {
		panic(err)
	}

	_ = conn.Close()

	server := grpc.NewServer()

	v1.RegisterSeederWithMetaServer(server, NewSeederWithMetaServiceGrpc(NewSeederWithMetaService(svc, b, agentVSockPort, w.verbose)))

	lis, err := net.Listen("tcp", net.JoinHostPort(w.lhost, "0"))
	if err != nil {
		panic(err)
	}

	i.lis = lis

	if w.verbose {
		log.Println("Seeding on", lis.Addr())
	}

	var serverWg sync.WaitGroup
	serverWg.Add(1)
	go func() {
		if err := server.Serve(lis); err != nil && !utils.IsClosedErr(err) {
			log.Printf("%v: %v", ErrCouldNotServeSeeder, err)
		}

		serverWg.Done()

		if outputRaddr != "" {
			w.instancesLock.Lock()
			delete(w.instances, outputRaddr)
			w.instancesLock.Unlock()
		}

		_ = i.close()
	}()

	i.server = server
	i.serverWg = &serverWg

	_, port, err := net.SplitHostPort(lis.Addr().String())
	if err != nil {
		panic(err)
	}

	outputRaddr = net.JoinHostPort(w.ahost, port)

	w.instancesLock.Lock()
	defer w.instancesLock.Unlock()

	w.instances[outputRaddr] = i

	return outputRaddr, nil
}

func (w *Worker) DeleteInstance(ctx context.Context, packageRaddr string) error {
	if w.verbose {
		log.Printf("DeleteInstance(packageRaddr = %v)", packageRaddr)
	}

	w.instancesLock.Lock()
	i, ok := w.instances[packageRaddr]
	if !ok {
		w.instancesLock.Unlock()

		return ErrCouldNotFindInstance
	}

	delete(w.instances, packageRaddr)
	w.instancesLock.Unlock()

	_ = i.close()

	return nil
}

func CloseWorker(w *Worker) error {
	w.instancesLock.Lock()
	defer w.instancesLock.Unlock()

	for _, i := range w.instances {
		_ = i.close()
	}

	w.instances = map[string]*instance{}

	return nil
}
