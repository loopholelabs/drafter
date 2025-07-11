package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	rfirecracker "github.com/loopholelabs/drafter/pkg/runtimes/firecracker"
	"github.com/loopholelabs/drafter/pkg/testutil"
	"github.com/loopholelabs/logging"
	"github.com/loopholelabs/logging/types"
	"github.com/muesli/gotable"
)

// main
func main() {
	log := logging.New(logging.Zerolog, "test", os.Stderr)
	log.SetLevel(types.InfoLevel)

	profileCPU := flag.Bool("prof", false, "Profile CPU")

	// Directory config
	dTestDir := flag.String("testdir", "testdir", "Test directory")
	dSnapDir := flag.String("snapdir", "snapdir", "Snap directory")
	dBlueDir := flag.String("bluedir", "bluedir", "Blue directory")
	noCleanup := flag.Bool("no-cleanup", false, "If true, then don't remove any files at the end")

	// VM options
	ioEngineSync := flag.Bool("sync", true, "Firecracker io engine sync")

	cpuCount := flag.Int("cpus", 1, "CPU count")
	memCount := flag.Int("memory", 1024, "Memory MB")

	template, err := rfirecracker.GetCPUTemplate()
	if err != nil {
		panic(err)
	}
	cpuTemplate := flag.String("template", template, "CPU Template")
	isPVM, err := rfirecracker.IsPVMHost()
	if err != nil {
		panic(err)
	}
	usePVMBootArgs := flag.Bool("pvm", isPVM, "PVM boot args")
	enableOutput := flag.Bool("enable-output", false, "Enable VM output")
	enableInput := flag.Bool("enable-input", false, "Enable VM input")

	// No silo
	runWithNonSilo := flag.Bool("nosilo", false, "Run a test with Silo disabled")

	iterations := flag.Int("count", 1, "Number of times to run each config")

	comp := true

	defaultConfigs, err := json.Marshal([]*RunConfig{
		{
			Name:          "silo_plain",
			BlockSize:     1024 * 1024,
			UseCow:        true,
			UseSharedBase: true,
			UseSparseFile: false,
			UseVolatility: false,
			NoMapShared:   true,
			MigrateAfter:  "90s",
			DirectMemory:  true,

			MigrationCompression: comp,
		},

		{
			Name:          "silo",
			BlockSize:     1024 * 1024,
			UseCow:        true,
			UseSharedBase: true,
			UseSparseFile: false,
			UseVolatility: false,
			NoMapShared:   true,
			MigrateAfter:  "90s",
			DirectMemory:  true,

			MigrationCompression: comp,
		},
	})

	if err != nil {
		panic(err)
	}

	runConfigs := flag.String("silo", string(defaultConfigs), "Run configs")

	valkeyTest := flag.Bool("valkey", false, "Run valkey benchmark test")
	valkeyIterations := flag.Int("valkeynum", 1000, "Test iterations")

	flag.Parse()

	var siloBaseConfigs []*RunConfig
	err = json.Unmarshal([]byte(*runConfigs), &siloBaseConfigs)
	if err != nil {
		panic(err)
	}

	log.Info().Str("runConfig", *runConfigs).Msg("using run configs")

	var siloConfigs []*RunConfig

	if *iterations == 1 {
		siloConfigs = siloBaseConfigs
	} else {
		for _, c := range siloBaseConfigs {
			for n := 0; n < *iterations; n++ {
				siloConfigs = append(siloConfigs, &RunConfig{
					Name:          fmt.Sprintf("%s.%d", c.Name, n),
					UseCow:        c.UseCow,
					UseSharedBase: c.UseSharedBase,
					UseSparseFile: c.UseSparseFile,
					UseVolatility: c.UseVolatility,
					UseWriteCache: c.UseWriteCache,

					WriteCacheMin:       c.WriteCacheMin,
					WriteCacheMax:       c.WriteCacheMax,
					WriteCacheBlocksize: c.WriteCacheBlocksize,

					BlockSize:    c.BlockSize,
					GrabPeriod:   c.GrabPeriod,
					NoMapShared:  c.NoMapShared,
					DirectMemory: c.DirectMemory,

					MigrateAfter:         c.MigrateAfter,
					MigrationCompression: c.MigrationCompression,
					MigrationConcurrency: c.MigrationConcurrency,

					S3Sync:        c.S3Sync,
					S3Secure:      c.S3Secure,
					S3SecretKey:   c.S3SecretKey,
					S3Endpoint:    c.S3Endpoint,
					S3Concurrency: c.S3Concurrency,
					S3Bucket:      c.S3Bucket,
					S3AccessKey:   c.S3AccessKey,
					S3BlockShift:  c.S3BlockShift,
					S3OnlyDirty:   c.S3OnlyDirty,
					S3MaxAge:      c.S3MaxAge,
					S3MinChanged:  c.S3MinChanged,
					S3Limit:       c.S3Limit,
					S3CheckPeriod: c.S3CheckPeriod,
				})
			}
		}
	}

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

	type portForward struct {
		PortSrc int
		PortDst int
	}

	// Default ports for start/stop oci
	forwards := []portForward{{PortSrc: 4567, PortDst: 4567}, {PortSrc: 4568, PortDst: 4568}}

	waitReady := func() error { return nil }

	// If we're just doing valkey, then we need to forward 6379 instead
	if *valkeyTest {
		forwards = []portForward{{PortSrc: 6379, PortDst: 3333}}
		valkeyWaitReady := &ValkeyWaitReady{Timeout: 30 * time.Second}
		waitReady = valkeyWaitReady.Ready
	}

	// Setup something to provision namespace and forward ports.
	getNetNs := func() (string, func(), error) {
		namespace, err := ns.ClaimNamespace()
		if err != nil {
			return "", nil, err
		}
		return namespace, func() {
			err := ns.ReleaseNamespace(namespace)
			if err != nil {
				fmt.Printf("Could not release namespace %v\n", err)
			}
		}, nil
	}

	doForwards := func(namespace string) (func(), error) {
		// Forward any ports we need
		closers := make([]func(), 0)
		for _, f := range forwards {
			portCloser, err := ForwardPort(log, namespace, "tcp", f.PortSrc, f.PortDst)
			if err != nil {
				return nil, err
			}
			closers = append(closers, portCloser)
		}

		return func() {
			for _, c := range closers {
				c()
			}
		}, nil
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

	snapNs, snapNsCloser, err := getNetNs()
	if err != nil {
		panic(err)
	}

	fcloser, err := doForwards(snapNs)
	if err != nil {
		panic(err)
	}

	err = setupSnapshot(log, ctx, snapNs, vmConfig, *ioEngineSync, *dBlueDir, *dSnapDir, waitReady)
	if err != nil {
		panic(err)
	}

	fcloser()
	snapNsCloser()

	log.Info().Msg("Starting tests...")

	siloTimingsGet := make(map[string]time.Duration, 0)
	siloTimingsSet := make(map[string]time.Duration, 0)
	siloTimingsRuntime := make(map[string]time.Duration, 0)

	dummyMetrics := testutil.NewDummyMetrics()

	//	siloDGs := make(map[string]*devicegroup.DeviceGroup)

	// Start testing Silo confs
	for _, sConf := range siloConfigs {
		fmt.Printf("\nSTARTING SILO RUN - %s\n", sConf.Summary())
		var runtimeStart time.Time
		var runtimeEnd time.Time
		benchCB := func() {
			runtimeStart = time.Now()

			if *valkeyTest {
				siloSet, siloGet, err := benchValkey(*profileCPU, sConf.Name, 3333, *valkeyIterations)
				siloTimingsSet[sConf.Name] = siloSet
				siloTimingsGet[sConf.Name] = siloGet
				if err != nil {
					fmt.Printf("ERROR in benchValkey %v\n", err)
				}
				runtimeEnd = time.Now()
			} else {
				err = benchCICD(*profileCPU, sConf.Name, 1*time.Hour)
				if err != nil {
					fmt.Printf("ERROR in benchCICD %v\n", err)
				}
				runtimeEnd = time.Now()
			}
		}

		err := runSilo(ctx, log, dummyMetrics, *dTestDir, *dSnapDir, getNetNs, doForwards, benchCB, sConf, *enableInput, *enableOutput)
		if err != nil {
			fmt.Printf("ERROR in runSilo %v\n", err)
			panic(err) // Can't carry on
		}

		//		siloDGs[sConf.Name] = dg

		siloTimingsRuntime[sConf.Name] = runtimeEnd.Sub(runtimeStart)

		time.Sleep(10 * time.Second) // Let dust settle between runs.
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
		err = runNonSilo(ctx, log, *dTestDir, *dSnapDir, getNetNs, doForwards, benchCB, *enableInput, *enableOutput)
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
	/*
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

				showDeviceStats(dummyMetrics, fmt.Sprintf("%s-%d", conf.Name, 0))
				showDeviceStats(dummyMetrics, fmt.Sprintf("%s-%d", conf.Name, 1))

				// Close the devicegroup
				err = siloDGs[conf.Name].CloseAll()
				if err != nil {
					fmt.Printf("Error closing DG %v\n", err)
				}

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
	*/
}
