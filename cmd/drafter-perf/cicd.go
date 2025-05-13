package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime/pprof"
	"time"

	"github.com/loopholelabs/silo/pkg/storage"
	"github.com/loopholelabs/silo/pkg/storage/expose"
	"github.com/loopholelabs/silo/pkg/storage/modules"
	"github.com/tklauser/ps"
)

func benchCICD(profileCPU bool, name string, timeout time.Duration, grabPeriod time.Duration) error {
	err := portCallback(4568, timeout)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go checkProcess(ctx, grabPeriod)

	if profileCPU {
		f, err := os.Create(fmt.Sprintf("%s.prof", name))
		if err != nil {
			panic(err)
		}
		pprof.StartCPUProfile(f)
		defer func() {
			pprof.StopCPUProfile()
			f.Close()
		}()
	}

	return portCallback(4567, timeout)
}

func portCallback(port int, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	for {
		select {
		case <-ticker.C:
			// Try to connect to the cicd runner
			con, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
			if err == nil {
				data, err := io.ReadAll(con)
				if err != nil {
					return err
				}
				fmt.Printf("PORT %d said %s\n", port, data)
				con.Close()
				return nil
			}
		case <-ctx.Done():
			return errors.New("Never finished")
		}
	}
}

func checkProcess(ctx context.Context, grabPeriod time.Duration) {
	var pid int
	foundFC := false
	procs, err := ps.Processes()
	if err == nil {
		for _, p := range procs {
			if p.Command() == "firecracker" {
				// Now play with the process memory...
				pid = p.PID()
				foundFC = true
			}
		}
	}

	if !foundFC {
		fmt.Printf("Could not find firecracker process!\n")
		return
	}

	pm := expose.NewProcessMemory(pid)

	start, end, err := pm.GetMemoryRange("/memory")
	if err != nil {
		return
	}

	size := uint64(end - start)
	pageSize := 1024

	ages := make([]time.Time, size/uint64(pageSize))

	snothing := modules.NewNothing(size)
	hooks := modules.NewHooks(snothing)
	hooks.PreWrite = func(buffer []byte, offset int64) (bool, int, error) {
		bStart := (int(offset) / pageSize)
		bEnd := ((int(offset) + len(buffer) - 1) / pageSize) + 1
		// Update our age map
		for b := bStart; b < bEnd; b++ {
			ages[b] = time.Now()
		}
		return false, len(buffer), nil
	}
	sprov := modules.NewMetrics(hooks)

	totalDowntime := time.Duration(0)
	maxDowntime := time.Duration(0)
	grabs := 0

	if grabPeriod > 0 {
		go func() {
			ticker := time.NewTicker(grabPeriod)
			for {
				select {
				case <-ctx.Done():
					// Print some summary here
					sprov.ShowStats("/memory")
					fmt.Printf("Total %d grabs downtime was %dms, Avg %dms, Max %dms\n", grabs, totalDowntime.Milliseconds(), totalDowntime.Milliseconds()/int64(grabs), maxDowntime.Milliseconds())

					// Look at the age of the memory....
					for b, r := range ages {
						if !r.IsZero() {
							fmt.Printf("BLOCKAGE %d %d\n", b, int(time.Since(r).Seconds()))
						}
					}

					return
				case <-ticker.C:
					ct := time.Now()
					err := grabDirtyMemory(pm, pid, sprov, start, end)
					dt := time.Since(ct)
					totalDowntime += dt
					grabs++
					if dt > maxDowntime {
						maxDowntime = dt
					}
					if err != nil {
						fmt.Printf("Error grabbing memory %v\n", err)
					}
				}
			}
		}()
	}
}

/**
 *
 *
 */
func grabDirtyMemory(pm *expose.ProcessMemory, pid int, prov storage.Provider, start uint64, end uint64) error {
	ctime := time.Now()

	pp := func() error {
		err := pm.PauseProcess()
		if err != nil {
			fmt.Printf("Error pausing process %v\n", err)
		}
		return err
	}
	rp := func() error {
		err := pm.ClearSoftDirty()
		if err != nil {
			fmt.Printf("Error clearing soft dirty %v\n", err)
			return err
		}

		err = pm.ResumeProcess()
		if err != nil {
			fmt.Printf("Error pausing process %v\n", err)
		}
		return err
	}

	// Read the soft dirty memory ranges
	ranges, err := pm.ReadSoftDirtyMemoryRangeList(start, end, pp, rp)
	if err != nil {
		return err
	}
	/*
		n := uint64(0)
		for _, r := range ranges {
			n += (r.End - r.Start)
		}
	*/
	n, err := pm.CopyMemoryRanges(start, ranges, prov)

	/*
		pp()

		fmt.Printf("CopySoftDirtyMemory start %016x end %016x\n", start, end)

		n, err := pm.CopySoftDirtyMemory(start, end, prov)
		rp()
	*/
	if err != nil {
		return err
	}

	fmt.Printf("ReadSoftDirtyMemory took %dms %d bytes\n", time.Since(ctime).Milliseconds(), n)
	return nil
}
