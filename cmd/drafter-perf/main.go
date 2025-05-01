package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	rfirecracker "github.com/loopholelabs/drafter/pkg/runtimes/firecracker"
	"github.com/loopholelabs/drafter/pkg/testutil"
	"github.com/loopholelabs/logging"
	"github.com/loopholelabs/logging/types"
	"github.com/muesli/gotable"
)

/**
 *
 * main
 */
func main() {
	log := logging.New(logging.Zerolog, "test", os.Stderr)
	log.SetLevel(types.ErrorLevel)

	profileCPU := flag.Bool("prof", false, "Profile CPU")
	dTestDir := flag.String("testdir", "testdir", "Test directory")
	dSnapDir := flag.String("snapdir", "snapdir", "Snap directory")
	dBlueDir := flag.String("bluedir", "bluedir", "Blue directory")

	cpuCount := flag.Int("cpus", 1, "CPU count")
	memCount := flag.Int("memory", 1024, "Memory MB")
	cpuTemplate := flag.String("template", "None", "CPU Template")
	usePVMBootArgs := flag.Bool("pvm", false, "PVM boot args")
	runWithNonSilo := flag.Bool("nosilo", false, "Run a test with Silo disabled")

	runSiloWC := flag.Bool("silowc", false, "Run a test with Silo WriteCache")
	wcMin := flag.String("wcmin", "80m", "Min writeCache size")
	wcMax := flag.String("wcmax", "100m", "Max writeCache size")

	runSiloAll := flag.Bool("silo", false, "Run all silo tests")

	valkeyTest := flag.Bool("valkey", false, "Run valkey benchmark test")
	valkeyIterations := flag.Int("valkeynum", 1000, "Test iterations")

	enableOutput := flag.Bool("enable-output", false, "Enable VM output")
	enableInput := flag.Bool("enable-input", false, "Enable VM input")

	flag.Parse()

	err := os.Mkdir(*dTestDir, 0777)
	if err != nil {
		panic(err)
	}
	defer func() {
		os.RemoveAll(*dTestDir)
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ns, natCloser, err := SetupNAT("", "dra")
	if err != nil {
		panic(err)
	}
	defer natCloser()

	netns, err := ns.ClaimNamespace()
	if err != nil {
		panic(err)
	}

	// Forward the port so we can connect to it...
	if *valkeyTest {
		portCloser, err := ForwardPort(log, netns, "tcp", 6379, 3333)
		if err != nil {
			panic(err)
		}
		defer portCloser()
	} else {
		portCloser1, err := ForwardPort(log, netns, "tcp", 4567, 4567)
		if err != nil {
			panic(err)
		}
		defer portCloser1()

		portCloser2, err := ForwardPort(log, netns, "tcp", 4568, 4568)
		if err != nil {
			panic(err)
		}
		defer portCloser2()

	}

	vmConfig := rfirecracker.VMConfiguration{
		CPUCount:    int64(*cpuCount),
		MemorySize:  int64(*memCount),
		CPUTemplate: *cpuTemplate,
		BootArgs:    rfirecracker.DefaultBootArgsNoPVM,
	}

	if *usePVMBootArgs {
		vmConfig.BootArgs = rfirecracker.DefaultBootArgs
	}

	// Clear the snap dir...
	os.RemoveAll(*dSnapDir)
	os.Mkdir(*dSnapDir, 0666)

	valkeyUp := false

	waitReady := func() {
		if *valkeyTest {
			// Try to connect to valkey
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			ticker := time.NewTicker(1 * time.Second)
			for {
				select {
				case <-ticker.C:
					// Try to connect to valkey
					con, err := net.Dial("tcp", "127.0.0.1:3333")
					if err == nil {
						con.Close()
						fmt.Printf(" ### Valkey up!\n")
						valkeyUp = true
						return
					}
				case <-ctx.Done():
					fmt.Printf(" ### Unable to connect to valkey!\n")
					return
				}
			}
		}
	}
	err = setupSnapshot(log, ctx, netns, vmConfig, *dBlueDir, *dSnapDir, waitReady)
	if err != nil {
		panic(err)
	}

	// Make sure valkey came up
	if *valkeyTest && !valkeyUp {
		panic(errors.New("Could not start valkey?"))
	}

	fmt.Printf("\n\n Starting tests...\n\n")

	siloTimingsGet := make(map[string]time.Duration, 0)
	siloTimingsSet := make(map[string]time.Duration, 0)
	siloTimingsRuntime := make(map[string]time.Duration, 0)

	dummyMetrics := testutil.NewDummyMetrics()

	siloConfigs := []siloConfig{}

	defaultBS := uint32(1024 * 64)

	if *runSiloAll {
		siloConfigs = []siloConfig{

			{name: "silo", blockSize: defaultBS, useCow: true, useSparseFile: true, useVolatility: true, useWriteCache: false},
			//				{name: "silo_no_vm_no_cow", blockSize: defaultBS, useCow: false, useSparseFile: false, useVolatility: false, useWriteCache: false},
			//				{name: "silo_no_vmsf", blockSize: defaultBS, useCow: true, useSparseFile: false, useVolatility: false, useWriteCache: false},
			//				{name: "silo_wc_80m_100m", blockSize: defaultBS, useCow: true, useSparseFile: false, useVolatility: false, useWriteCache: true, writeCacheMin: "80m", writeCacheMax: "100m"},
			//				{name: "silo_wc_200m_400m", blockSize: defaultBS, useCow: true, useSparseFile: false, useVolatility: false, useWriteCache: true, writeCacheMin: "200m", writeCacheMax: "400m"},
			//				{name: "silo_wc_600m_800m", blockSize: defaultBS, useCow: true, useSparseFile: false, useVolatility: false, useWriteCache: true, writeCacheMin: "600m", writeCacheMax: "800m"},
			//				{name: "silo_wc_800m_1g", blockSize: defaultBS, useCow: true, useSparseFile: false, useVolatility: false, useWriteCache: true, writeCacheMin: "800m", writeCacheMax: "1g"},

			/*
				{name: "silo_4k", blockSize: 4 * 1024, useCow: true, useSparseFile: false, useVolatility: false, useWriteCache: false},
				{name: "silo_8k", blockSize: 8 * 1024, useCow: true, useSparseFile: false, useVolatility: false, useWriteCache: false},
				{name: "silo_16k", blockSize: 16 * 1024, useCow: true, useSparseFile: false, useVolatility: false, useWriteCache: false},
				{name: "silo_32k", blockSize: 32 * 1024, useCow: true, useSparseFile: false, useVolatility: false, useWriteCache: false},
				{name: "silo_64k", blockSize: 64 * 1024, useCow: true, useSparseFile: false, useVolatility: false, useWriteCache: false},
				{name: "silo_128k", blockSize: 128 * 1024, useCow: true, useSparseFile: false, useVolatility: false, useWriteCache: false},
				{name: "silo_256k", blockSize: 256 * 1024, useCow: true, useSparseFile: false, useVolatility: false, useWriteCache: false},
				{name: "silo_512k", blockSize: 512 * 1024, useCow: true, useSparseFile: false, useVolatility: false, useWriteCache: false},
				{name: "silo_1m", blockSize: 1024 * 1024, useCow: true, useSparseFile: false, useVolatility: false, useWriteCache: false},
			*/
		}
	} else if *runSiloWC {
		siloConfigs = append(siloConfigs, siloConfig{
			name: fmt.Sprintf("silo_wc_%s_%s", *wcMin, *wcMax), blockSize: defaultBS, useCow: true, useSparseFile: false, useVolatility: false, useWriteCache: true, writeCacheMin: *wcMin, writeCacheMax: *wcMax,
		})
	}

	// Start testing Silo confs
	for _, sConf := range siloConfigs {
		var runtimeStart time.Time
		var runtimeEnd time.Time
		benchCB := func() {
			runtimeStart = time.Now()

			if *valkeyTest {
				siloSet, siloGet, err := benchValkey(*profileCPU, sConf.name, 3333, *valkeyIterations)
				siloTimingsSet[sConf.name] = siloSet
				siloTimingsGet[sConf.name] = siloGet
				if err != nil {
					panic(err)
				}
				runtimeEnd = time.Now()
			} else {
				err = benchCICD(*profileCPU, sConf.name, 1*time.Hour)
				if err != nil {
					panic(err)
				}
				runtimeEnd = time.Now()
			}
		}

		err = runSilo(ctx, log, dummyMetrics, *dTestDir, *dSnapDir, netns, benchCB, sConf, *enableInput, *enableOutput)
		if err != nil {
			panic(err)
		}
		siloTimingsRuntime[sConf.name] = runtimeEnd.Sub(runtimeStart)
	}

	var nosiloGet time.Duration
	var nosiloSet time.Duration
	var nosiloRuntime time.Duration

	if *runWithNonSilo {
		benchCB := func() {
			ctime := time.Now()
			if *valkeyTest {
				nosiloSet, nosiloGet, err = benchValkey(*profileCPU, "nosilo", 3333, *valkeyIterations)
			} else {
				err = benchCICD(*profileCPU, "nosilo", 1*time.Hour)
			}
			if err != nil {
				panic(err)
			}
			nosiloRuntime = time.Since(ctime)
		}
		err = runNonSilo(ctx, log, *dTestDir, *dSnapDir, netns, benchCB, *enableInput, *enableOutput)
		if err != nil {
			panic(err)
		}
	}

	// Now we print out summary etc
	// Work out rough overhead here...

	fmt.Printf("\n### Results ###\n\n")

	tab := gotable.NewTable([]string{"Name", "WriteC", "vm", "Cow", "SparseF", "Runtime", "RuntimeOver"},
		[]int64{-20, 8, 4, 4, 8, 10, 12}, "No data in table.")

	if *valkeyTest {
		tab = gotable.NewTable([]string{"Name", "WriteC", "vm", "Cow", "SparseF",
			"Set time", "SetOver", "Get time", "GetOver", "Runtime", "RuntimeOver"},
			[]int64{-20, 8, 4, 4, 8, 10, 8, 10, 8, 10, 12}, "No data in table.")
	}

	if *valkeyTest {
		tab.AppendRow([]interface{}{"No Silo", "", "", "", "",
			fmt.Sprintf("%.1fs", float64(nosiloSet.Milliseconds())/1000), "",
			fmt.Sprintf("%.1fs", float64(nosiloGet.Milliseconds())/1000), "",
			fmt.Sprintf("%.1fs", float64(nosiloRuntime.Milliseconds())/1000), "",
		})
	} else {
		tab.AppendRow([]interface{}{"No Silo", "", "", "", "",
			fmt.Sprintf("%.1fs", float64(nosiloRuntime.Milliseconds())/1000), "",
		})
	}

	fbool := func(b bool) string {
		if b {
			return "YES"
		} else {
			return ""
		}
	}

	for _, conf := range siloConfigs {
		// Show some device stats

		fmt.Printf("== Results for %s\n", conf.Summary())

		showDeviceStats(dummyMetrics, conf.name)

		if *valkeyTest {

			siloSet := siloTimingsSet[conf.name]
			siloGet := siloTimingsGet[conf.name]
			overheadSet := 0
			overheadGet := 0
			overhead := 0
			if nosiloRuntime != 0 {
				overhead = int((siloTimingsRuntime[conf.name] - nosiloRuntime) * 100 / nosiloRuntime)
			}
			if nosiloSet != 0 {
				overheadSet = int((siloSet - nosiloSet) * 100 / nosiloSet)
			}
			if nosiloGet != 0 {
				overheadGet = int((siloGet - nosiloGet) * 100 / nosiloGet)
			}

			tab.AppendRow([]interface{}{conf.name,
				fbool(conf.useWriteCache), fbool(conf.useVolatility), fbool(conf.useCow), fbool(conf.useSparseFile),
				fmt.Sprintf("%.1fs", float64(siloSet.Milliseconds())/1000), fmt.Sprintf("%d%%", overheadSet),
				fmt.Sprintf("%.1fs", float64(siloGet.Milliseconds())/1000), fmt.Sprintf("%d%%", overheadGet),
				fmt.Sprintf("%.1fs", float64(siloTimingsRuntime[conf.name].Milliseconds())/1000), fmt.Sprintf("%d%%", overhead),
			})

		} else {
			overhead := 0
			if nosiloRuntime != 0 {
				overhead = int((siloTimingsRuntime[conf.name] - nosiloRuntime) * 100 / nosiloRuntime)
			}

			tab.AppendRow([]interface{}{conf.name,
				fbool(conf.useWriteCache), fbool(conf.useVolatility), fbool(conf.useCow), fbool(conf.useSparseFile),
				fmt.Sprintf("%.1fs", float64(siloTimingsRuntime[conf.name].Milliseconds())/1000), fmt.Sprintf("%d%%", overhead),
			})
		}
	}

	tab.Print()
}

type DeviceMetrics struct {
	DiskReadOps    uint64
	DiskReadBytes  uint64
	DiskWriteOps   uint64
	DiskWriteBytes uint64
	InReadOps      uint64
	InReadBytes    uint64
	InWriteOps     uint64
	InWriteBytes   uint64
	ChangedBlocks  uint64
	ChangedBytes   uint64
}

/**
 * Grab out some silo stats from the metrics system
 *
 */
func getSiloDeviceStats(dummyMetrics *testutil.DummyMetrics, name string, deviceName string) *DeviceMetrics {
	metrics := dummyMetrics.GetMetrics(name, deviceName).GetMetrics()
	devMetrics := dummyMetrics.GetMetrics(name, fmt.Sprintf("device_%s", deviceName)).GetMetrics()
	rodev := dummyMetrics.GetMetrics(name, fmt.Sprintf("device_rodev_%s", deviceName))

	// Now grab out what we need from these...
	dm := &DeviceMetrics{
		InReadOps:      metrics.ReadOps,
		InReadBytes:    metrics.ReadBytes,
		InWriteOps:     metrics.WriteOps,
		InWriteBytes:   metrics.WriteBytes,
		DiskReadOps:    devMetrics.ReadOps,
		DiskReadBytes:  devMetrics.ReadBytes,
		DiskWriteOps:   devMetrics.WriteOps,
		DiskWriteBytes: devMetrics.WriteBytes,
	}

	// If we are using COW, then add these on to the totals.
	if rodev != nil {
		devROMetrics := rodev.GetMetrics()

		dm.DiskReadOps += devROMetrics.ReadOps
		dm.DiskReadBytes += devROMetrics.ReadBytes
		dm.DiskWriteOps += devROMetrics.WriteOps
		dm.DiskWriteBytes += devROMetrics.WriteBytes

		cow := dummyMetrics.GetCow(fmt.Sprintf("post_%s", name), deviceName)

		if cow != nil {
			changedBlocks, changedBytes, err := cow.GetDifference()
			if err == nil {
				dm.ChangedBlocks = uint64(changedBlocks)
				dm.ChangedBytes = uint64(changedBytes)
			}
		}

	}
	return dm
}

func showDeviceStats(dummyMetrics *testutil.DummyMetrics, name string) {
	devTab := gotable.NewTable([]string{"Name",
		"In R Ops", "In R MB", "In W Ops", "In W MB",
		"DskR Ops", "DskR MB", "DskW Ops", "DskW MB",
		"Chg  Blk", "Chg  MB",
	},
		[]int64{-20,
			8, 8, 8, 8,
			8, 8, 8, 8,
			8, 8,
		},
		"No data in table.")

	for _, r := range []string{"disk", "oci", "memory"} {
		dm := getSiloDeviceStats(dummyMetrics, name, r)
		devTab.AppendRow([]interface{}{
			r,
			fmt.Sprintf("%d", dm.InReadOps),
			fmt.Sprintf("%.1f", float64(dm.InReadBytes)/(1024*1024)),
			fmt.Sprintf("%d", dm.InWriteOps),
			fmt.Sprintf("%.1f", float64(dm.InWriteBytes)/(1024*1024)),

			fmt.Sprintf("%d", dm.DiskReadOps),
			fmt.Sprintf("%.1f", float64(dm.DiskReadBytes)/(1024*1024)),
			fmt.Sprintf("%d", dm.DiskWriteOps),
			fmt.Sprintf("%.1f", float64(dm.DiskWriteBytes)/(1024*1024)),

			fmt.Sprintf("%d", dm.ChangedBlocks),
			fmt.Sprintf("%.1f", float64(dm.ChangedBytes)/(1024*1024)),
		})
	}

	devTab.Print()

	fmt.Printf("\n")

}
