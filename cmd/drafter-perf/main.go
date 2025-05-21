package main

import (
	"context"
	"encoding/json"
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
	log.SetLevel(types.InfoLevel)

	profileCPU := flag.Bool("prof", false, "Profile CPU")
	dTestDir := flag.String("testdir", "testdir", "Test directory")
	dSnapDir := flag.String("snapdir", "snapdir", "Snap directory")
	dBlueDir := flag.String("bluedir", "bluedir", "Blue directory")
	noCleanup := flag.Bool("no-cleanup", false, "If true, then don't remove any files at the end")

	cpuCount := flag.Int("cpus", 1, "CPU count")
	memCount := flag.Int("memory", 1024, "Memory MB")
	cpuTemplate := flag.String("template", "None", "CPU Template")
	usePVMBootArgs := flag.Bool("pvm", false, "PVM boot args")

	// No silo
	runWithNonSilo := flag.Bool("nosilo", false, "Run a test with Silo disabled")

	defaultConfigs, err := json.Marshal([]RunConfig{
		{Name: "silo", BlockSize: 1024 * 1024, UseCow: true, UseSparseFile: true, UseVolatility: true, UseWriteCache: false, GrabPeriod: 0},
		{Name: "silo_5s", BlockSize: 1024 * 1024, UseCow: true, UseSparseFile: true, UseVolatility: true, UseWriteCache: false, GrabPeriod: 5 * time.Second},
	})

	runConfigs := flag.String("silo", string(defaultConfigs), "Run configs")

	valkeyTest := flag.Bool("valkey", false, "Run valkey benchmark test")
	valkeyIterations := flag.Int("valkeynum", 1000, "Test iterations")

	enableOutput := flag.Bool("enable-output", false, "Enable VM output")
	enableInput := flag.Bool("enable-input", false, "Enable VM input")

	migrateAfter := flag.String("migrate-after", "", "Migrate the VM after a time period")

	flag.Parse()

	var siloConfigs []RunConfig
	err = json.Unmarshal([]byte(*runConfigs), &siloConfigs)
	if err != nil {
		panic(err)
	}

	log.Info().Str("runConfig", *runConfigs).Msg("using run configs")

	err = os.Mkdir(*dTestDir, 0777)
	if err != nil {
		panic(err)
	}
	if !*noCleanup {
		defer func() {
			os.RemoveAll(*dTestDir)
		}()
	}

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
	err = os.Mkdir(*dSnapDir, 0666)
	if err != nil {
		panic(err)
	}

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

	log.Info().Msg("Starting tests...")

	siloTimingsGet := make(map[string]time.Duration, 0)
	siloTimingsSet := make(map[string]time.Duration, 0)
	siloTimingsRuntime := make(map[string]time.Duration, 0)

	dummyMetrics := testutil.NewDummyMetrics()

	// Start testing Silo confs
	for _, sConf := range siloConfigs {
		var runtimeStart time.Time
		var runtimeEnd time.Time
		benchCB := func() {
			runtimeStart = time.Now()

			if *valkeyTest {
				siloSet, siloGet, err := benchValkey(*profileCPU, sConf.Name, 3333, *valkeyIterations)
				siloTimingsSet[sConf.Name] = siloSet
				siloTimingsGet[sConf.Name] = siloGet
				if err != nil {
					panic(err)
				}
				runtimeEnd = time.Now()
			} else {
				err = benchCICD(*profileCPU, sConf.Name, 1*time.Hour)
				if err != nil {
					panic(err)
				}
				runtimeEnd = time.Now()
			}
		}

		err = runSilo(ctx, log, dummyMetrics, *dTestDir, *dSnapDir, netns, benchCB, sConf, *enableInput, *enableOutput, *migrateAfter)
		if err != nil {
			panic(err)
		}
		siloTimingsRuntime[sConf.Name] = runtimeEnd.Sub(runtimeStart)
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

		showDeviceStats(dummyMetrics, conf.Name)

		if *valkeyTest {
			siloSet := siloTimingsSet[conf.Name]
			siloGet := siloTimingsGet[conf.Name]
			overheadSet := 0
			overheadGet := 0
			overhead := 0
			if nosiloRuntime != 0 {
				overhead = int((siloTimingsRuntime[conf.Name] - nosiloRuntime) * 100 / nosiloRuntime)
			}
			if nosiloSet != 0 {
				overheadSet = int((siloSet - nosiloSet) * 100 / nosiloSet)
			}
			if nosiloGet != 0 {
				overheadGet = int((siloGet - nosiloGet) * 100 / nosiloGet)
			}

			tab.AppendRow([]interface{}{conf.Name,
				fbool(conf.UseWriteCache), fbool(conf.UseVolatility), fbool(conf.UseCow), fbool(conf.UseSparseFile),
				fmt.Sprintf("%.1fs", float64(siloSet.Milliseconds())/1000), fmt.Sprintf("%d%%", overheadSet),
				fmt.Sprintf("%.1fs", float64(siloGet.Milliseconds())/1000), fmt.Sprintf("%d%%", overheadGet),
				fmt.Sprintf("%.1fs", float64(siloTimingsRuntime[conf.Name].Milliseconds())/1000), fmt.Sprintf("%d%%", overhead),
			})

		} else {
			overhead := 0
			if nosiloRuntime != 0 {
				overhead = int((siloTimingsRuntime[conf.Name] - nosiloRuntime) * 100 / nosiloRuntime)
			}

			tab.AppendRow([]interface{}{conf.Name,
				fbool(conf.UseWriteCache), fbool(conf.UseVolatility), fbool(conf.UseCow), fbool(conf.UseSparseFile),
				fmt.Sprintf("%.1fs", float64(siloTimingsRuntime[conf.Name].Milliseconds())/1000), fmt.Sprintf("%d%%", overhead),
			})
		}
	}

	tab.Print()
}
