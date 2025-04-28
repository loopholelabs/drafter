package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path"
	"time"

	"github.com/loopholelabs/drafter/pkg/common"
	rfirecracker "github.com/loopholelabs/drafter/pkg/runtimes/firecracker"
	"github.com/loopholelabs/drafter/pkg/testutil"
	"github.com/loopholelabs/logging"
	"github.com/loopholelabs/logging/types"
	"github.com/loopholelabs/silo/pkg/storage"
	"github.com/loopholelabs/silo/pkg/storage/sources"
	"github.com/muesli/gotable"
)

const dBlueDir = "out/blueprint"
const profileCPU = true

/**
 *
 * main
 */
func main() {
	log := logging.New(logging.Zerolog, "test", os.Stderr)
	log.SetLevel(types.ErrorLevel)

	dTestDir := flag.String("testdir", "testdir", "Test directory")
	dSnapDir := flag.String("snapdir", "snapdir", "Snap directory")
	dBlueDir := flag.String("bluedir", "bluedir", "Blue directory")

	cpuCount := flag.Int("cpus", 1, "CPU count")
	memCount := flag.Int("memory", 1024, "Memory MB")
	cpuTemplate := flag.String("template", "None", "CPU Template")
	usePVMBootArgs := flag.Bool("pvm", false, "PVM boot args")

	iterations := flag.Int("num", 1000, "Test iterations")

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
	portCloser, err := ForwardPort(log, netns, "tcp", 6379, 3333)
	if err != nil {
		panic(err)
	}
	defer portCloser()

	vmConfig := rfirecracker.VMConfiguration{
		CPUCount:    int64(*cpuCount),
		MemorySize:  int64(*memCount),
		CPUTemplate: *cpuTemplate,
		BootArgs:    rfirecracker.DefaultBootArgsNoPVM,
	}

	if *usePVMBootArgs {
		vmConfig.BootArgs = rfirecracker.DefaultBootArgs
	}

	waitReady := func() {} // TODO: Have this wait for conneciton to work
	err = setupSnapshot(log, ctx, netns, vmConfig, *dBlueDir, *dSnapDir, waitReady)
	if err != nil {
		panic(err)
	}

	siloTimingsGet := make(map[string]time.Duration, 0)
	siloTimingsSet := make(map[string]time.Duration, 0)
	siloTimingsRuntime := make(map[string]time.Duration, 0)

	dummyMetrics := testutil.NewDummyMetrics()

	siloConfigs := []siloConfig{
		{name: "silo", useCow: true, useSparseFile: true, useVolatility: true, useWriteCache: false},
		{name: "silo_no_vm_no_cow", useCow: false, useSparseFile: false, useVolatility: false, useWriteCache: false},
		{name: "silo_no_vmsf", useCow: true, useSparseFile: false, useVolatility: false, useWriteCache: false},
		{name: "silo_no_vmsf_wc", useCow: true, useSparseFile: false, useVolatility: false, useWriteCache: true},
	}

	// Start testing Silo confs
	for _, sConf := range siloConfigs {
		var runtimeStart time.Time
		benchCB := func() {
			runtimeStart = time.Now()
			siloSet, siloGet, err := benchValkey(sConf.name, 3333, *iterations)
			siloTimingsSet[sConf.name] = siloSet
			siloTimingsGet[sConf.name] = siloGet
			if err != nil {
				panic(err)
			}
		}

		err = runSilo(ctx, log, dummyMetrics, *dTestDir, *dSnapDir, netns, benchCB, sConf)
		if err != nil {
			panic(err)
		}
		siloTimingsRuntime[sConf.name] = time.Since(runtimeStart)
	}

	var nosiloGet time.Duration
	var nosiloSet time.Duration
	var nosiloRuntime time.Duration
	benchCB := func() {
		ctime := time.Now()
		nosiloSet, nosiloGet, err = benchValkey("nosilo", 3333, *iterations)
		nosiloRuntime = time.Since(ctime)
		if err != nil {
			panic(err)
		}
	}
	err = runNonSilo(ctx, log, *dTestDir, *dSnapDir, netns, benchCB)
	if err != nil {
		panic(err)
	}

	// Now we print out summary etc
	// Work out rough overhead here...

	fmt.Printf("\n### Results ###\n\n")

	tab := gotable.NewTable([]string{"Name", "WriteC", "vm", "Cow", "SparseF",
		"Set time", "SetOver", "Get time", "GetOver", "Runtime",
		"memROps", "memRMB", "memWOps", "memWMB",
		"memChgBlk", "memChgMB",
	},
		[]int64{-20,
			8, 4, 4, 8,
			10, 8, 10, 8,
			10, // runtime
			8, 8, 8, 8,
			12, 12,
		},
		"No data in table.")

	tab.AppendRow([]interface{}{"No Silo", "", "", "", "",
		fmt.Sprintf("%.1fs", float64(nosiloSet.Milliseconds())/1000), "",
		fmt.Sprintf("%.1fs", float64(nosiloGet.Milliseconds())/1000), "",
		fmt.Sprintf("%.1fs", float64(nosiloRuntime.Milliseconds())/1000), "",
		"", "", "", "", ""})

	fbool := func(b bool) string {
		if b {
			return "YES"
		} else {
			return ""
		}
	}

	for _, conf := range siloConfigs {
		siloSet := siloTimingsSet[conf.name]
		siloGet := siloTimingsGet[conf.name]
		overheadSet := (siloSet - nosiloSet) * 100 / nosiloSet
		overheadGet := (siloGet - nosiloGet) * 100 / nosiloGet

		//		memMetrics := dummyMetrics.GetMetrics(conf.name, "memory").GetMetrics()
		memDMetrics := dummyMetrics.GetMetrics(conf.name, "device_memory").GetMetrics()

		// Show some metrics
		for _, r := range []string{"memory"} { // "disk", "oci", "memory"} {
			m := dummyMetrics.GetMetrics(conf.name, r)
			m.ShowStats(fmt.Sprintf("%s_%s", conf.name, r))

			m = dummyMetrics.GetMetrics(conf.name, fmt.Sprintf("device_%s", r))
			m.ShowStats(fmt.Sprintf("dev_%s_%s", conf.name, r))

			m = dummyMetrics.GetMetrics(conf.name, fmt.Sprintf("device_rodev_%s", r))
			if m != nil {
				m.ShowStats(fmt.Sprintf("dev_%s_rodev_%s", conf.name, r))
			}
		}

		memChangedBlocks := uint64(0)
		memChangedBytes := uint64(0)

		// Check how much data changed in memory device here...
		if conf.useCow && !conf.useSparseFile {
			n := "memory"
			fn := common.DeviceFilenames[n]
			size := int64(*memCount * 1024 * 1024)
			baseFile := path.Join(*dSnapDir, n)
			sp0, err := sources.NewFileStorage(baseFile, size)
			if err != nil {
				panic(err)
			}
			overlayFile := path.Join(path.Join(*dTestDir, conf.name, fmt.Sprintf("%s.overlay", fn)))
			var sp1 storage.Provider
			blockSize := 1024 * 1024
			if conf.useSparseFile {
				sp1, err = sources.NewFileStorageSparse(overlayFile, uint64(size), blockSize)
				if err != nil {
					fmt.Printf("Error %s %v\n", overlayFile, err)
				}
			} else {
				sp1, err = sources.NewFileStorage(overlayFile, size)
				if err != nil {
					panic(err)
				}
			}

			if sp0 != nil && sp1 != nil {
				memChangedBlocks, memChangedBytes, err = storage.Difference(sp0, sp1, blockSize)
				fmt.Printf("%s Memory base %d bytes, changed %d bytes, %d blocks.\n", conf.name, size, memChangedBytes, memChangedBlocks)
			}
		}

		tab.AppendRow([]interface{}{conf.name,
			fbool(conf.useWriteCache), fbool(conf.useVolatility), fbool(conf.useCow), fbool(conf.useSparseFile),
			fmt.Sprintf("%.1fs", float64(siloSet.Milliseconds())/1000), fmt.Sprintf("%d%%", overheadSet),
			fmt.Sprintf("%.1fs", float64(siloGet.Milliseconds())/1000), fmt.Sprintf("%d%%", overheadGet),
			fmt.Sprintf("%.1fs", float64(siloTimingsRuntime[conf.name].Milliseconds())/1000),
			fmt.Sprintf("%d", memDMetrics.ReadOps),
			fmt.Sprintf("%.1f", float64(memDMetrics.ReadBytes)/(1024*1024)),
			fmt.Sprintf("%d", memDMetrics.WriteOps),
			fmt.Sprintf("%.1f", float64(memDMetrics.WriteBytes)/(1024*1024)),
			fmt.Sprintf("%d", memChangedBlocks),
			fmt.Sprintf("%.1f", float64(memChangedBytes)/(1024*1024)),
		})

	}

	tab.Print()

}
