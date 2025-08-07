package runtimes

import (
	"context"
	"fmt"
	"io"
	"path"
	"sync"
	"time"

	"github.com/loopholelabs/drafter/pkg/common"
	"github.com/loopholelabs/logging/types"
	"github.com/loopholelabs/silo/pkg/storage"
	"github.com/loopholelabs/silo/pkg/storage/devicegroup"
	"github.com/loopholelabs/silo/pkg/storage/modules"
	"github.com/loopholelabs/silo/pkg/storage/sources"
)

// A ReplayRuntimeProvider will replay a binlog set
type ReplayRuntimeProvider struct {
	HomePath    string
	ReplayPath  string
	DeviceSizes map[string]int
	Speed       float64
	Log         types.Logger
	Zipped      bool

	providers    map[string]storage.Provider
	replayers    map[string]*modules.BinLogReplay
	replayCtx    context.Context
	replayCancel context.CancelFunc
	replayWg     sync.WaitGroup
}

func (rp *ReplayRuntimeProvider) init() error {

	names := common.KnownNames

	rp.replayers = make(map[string]*modules.BinLogReplay)
	rp.providers = make(map[string]storage.Provider)
	for _, d := range names {
		var prov storage.Provider
		var err error
		filename := d
		if rp.Zipped {
			filename = fmt.Sprintf("%s.gz", d)
		}
		prov, err = sources.NewFileStorage(path.Join(rp.HomePath, d), int64(rp.DeviceSizes[d]))
		if err != nil {
			return err
		}

		if rp.Log != nil {
			prov = modules.NewLogger(prov, d, rp.Log)
		}

		rp.providers[d] = prov
		// Setup a replay log
		replay, err := modules.NewBinLogReplay(path.Join(rp.ReplayPath, filename), prov)
		if err != nil {
			return err
		}
		rp.replayers[d] = replay
	}
	return nil
}

func (rp *ReplayRuntimeProvider) Start(ctx context.Context, rescueCtx context.Context, errChan chan error) error {
	return nil
}

func (rp *ReplayRuntimeProvider) Close(dg *devicegroup.DeviceGroup) error {
	err := rp.Suspend(context.TODO(), 10*time.Second, dg)
	if err != nil {
		return err
	}
	for n, prov := range rp.providers {
		err := prov.Close()
		if err != nil {
			fmt.Printf("Error closing provider %s %v\n", n, err)
			return err
		}
	}
	return nil
}

func (rp *ReplayRuntimeProvider) DevicePath() string {
	return rp.HomePath
}

func (rp *ReplayRuntimeProvider) GetVMPid() int {
	return 0
}

func (rp *ReplayRuntimeProvider) Suspend(ctx context.Context, timeout time.Duration, dg *devicegroup.DeviceGroup) error {
	fmt.Printf("### Suspend\n")
	if rp.replayCancel != nil {
		rp.replayCancel()
		rp.replayWg.Wait()
		rp.replayCancel = nil
	}
	err := rp.FlushData(ctx, dg)
	fmt.Printf("### Suspend done\n")
	return err
}

func (rp *ReplayRuntimeProvider) FlushData(ctx context.Context, dg *devicegroup.DeviceGroup) error {
	fmt.Printf("### Flush data\n")
	for n, prov := range rp.providers {
		err := prov.Flush()
		if err != nil {
			fmt.Printf("Error flushing provider %s %v\n", n, err)
		}
	}
	return nil
}

func (rp *ReplayRuntimeProvider) FlushDevices(ctx context.Context, dg *devicegroup.DeviceGroup) error {
	fmt.Printf("### Flush devices\n")
	return nil
}

func (rp *ReplayRuntimeProvider) Resume(ctx context.Context, rescueTimeout time.Duration, dg *devicegroup.DeviceGroup, errChan chan error) error {
	fmt.Printf("### Resume\n")
	if rp.replayers == nil {
		err := rp.init()
		if err != nil {
			fmt.Printf("### init error %v\n", err)
		}
	}

	rp.replayCtx, rp.replayCancel = context.WithCancel(context.Background())
	// Setup the replayers
	for n, replay := range rp.replayers {
		rp.replayWg.Add(1)
		go func(devName string, rep *modules.BinLogReplay) {
			defer rp.replayWg.Done()
			for {

				// If we're done, return
				select {
				case <-rp.replayCtx.Done():
					return
				default:
				}

				_, err := rep.Next(rp.Speed, true)
				if err == io.EOF {
					return // Done...
				}
				if err != nil {
					fmt.Printf("## Err replaying %v\n", err)
				}
			}
		}(n, replay)
	}
	return nil
}
