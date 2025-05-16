//go:build integration
// +build integration

package firecracker

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path"
	"runtime/pprof"
	"testing"
	"time"
	"unsafe"

	"github.com/google/uuid"
	"github.com/loopholelabs/drafter/pkg/common"
	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/drafter/pkg/peer"
	"github.com/loopholelabs/drafter/pkg/testutil"
	"github.com/loopholelabs/logging"
	"github.com/loopholelabs/logging/types"
	"github.com/loopholelabs/silo/pkg/storage"
	"github.com/loopholelabs/silo/pkg/storage/sources"
	"github.com/muesli/gotable"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	valkey "github.com/valkey-io/valkey-go"
)

const perfTestDir = "perf_test"

const profileCPU = true

/**
 * This creates a snapshot, and then runs some perf testing
 *
 * firecracker needs to work
 * blueprints expected to exist at ./out/blueprint
 *
 */
func TestValkeyPerf(t *testing.T) {
	t.Skip("Not running perf test for now")

	log := logging.New(logging.Zerolog, "test", os.Stderr)
	log.SetLevel(types.ErrorLevel)

	err := os.Mkdir(perfTestDir, 0777)
	assert.NoError(t, err)
	t.Cleanup(func() {
		os.RemoveAll(perfTestDir)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ns := testutil.SetupNAT(t, "", "dra")

	netns, err := ns.ClaimNamespace()
	assert.NoError(t, err)

	// Forward the port so we can connect to it...
	testutil.ForwardPort(t, log, netns, "tcp", 6379, 3333)

	snapDir := setupSnapshot(t, log, ctx, netns, VMConfiguration{
		CPUCount:    1,
		MemorySize:  1024,
		CPUTemplate: "None",
		BootArgs:    DefaultBootArgsNoPVM,
	},
	)

	agentVsockPort := uint32(26)
	agentLocal := struct{}{}

	deviceFiles := []string{
		"state", "memory", "kernel", "disk", "config", "oci",
	}

	devicesAt := snapDir

	firecrackerBin, err := exec.LookPath("firecracker")
	assert.NoError(t, err)

	jailerBin, err := exec.LookPath("jailer")
	assert.NoError(t, err)

	siloTimingsGet := make(map[string]time.Duration, 0)
	siloTimingsSet := make(map[string]time.Duration, 0)
	siloTimingsRuntime := make(map[string]time.Duration, 0)

	dummyMetrics := testutil.NewDummyMetrics()

	// var dummyMetrics metrics.SiloMetrics

	type siloConfig struct {
		name          string
		useCow        bool
		useSparseFile bool
		useVolatility bool
		useWriteCache bool
	}

	siloBaseConfigs := []siloConfig{
		{name: "silo", useCow: true, useSparseFile: true, useVolatility: true, useWriteCache: false},
		{name: "silo_no_vm_no_cow", useCow: false, useSparseFile: false, useVolatility: false, useWriteCache: false},
		{name: "silo_no_vmsf", useCow: true, useSparseFile: false, useVolatility: false, useWriteCache: false},
		{name: "silo_no_vmsf_wc", useCow: true, useSparseFile: false, useVolatility: false, useWriteCache: true},
	}

	// Do each one a few times maybe...
	siloConfigs := make([]siloConfig, 0)
	for u := 1; u < 2; u++ {
		for _, c := range siloBaseConfigs {
			siloConfigs = append(siloConfigs, siloConfig{
				name:          fmt.Sprintf("%s_%d", c.name, u),
				useCow:        c.useCow,
				useSparseFile: c.useSparseFile,
				useVolatility: c.useVolatility,
				useWriteCache: c.useWriteCache,
			})
		}
	}

	// Start testing Silo confs
	for _, sConf := range siloConfigs {

		// First try with Silo
		devicesFrom := make([]common.MigrateFromDevice, 0)
		for _, n := range append(common.KnownNames, "oci") {
			// Create some initial devices...
			fn := common.DeviceFilenames[n]

			dev := common.MigrateFromDevice{
				Name:       n,
				BlockSize:  1024 * 1024,
				Shared:     false,
				SharedBase: true,
				AnyOrder:   !sConf.useVolatility,
			}

			if sConf.useWriteCache && n == "memory" {
				dev.UseWriteCache = true
			}

			if sConf.useCow {
				dev.Base = path.Join(snapDir, n)
				dev.UseSparseFile = sConf.useSparseFile
				dev.Overlay = path.Join(path.Join(perfTestDir, sConf.name, fmt.Sprintf("%s.overlay", fn)))
				dev.State = path.Join(path.Join(perfTestDir, sConf.name, fmt.Sprintf("%s.state", fn)))
			} else {
				// Copy the file
				src, err := os.Open(path.Join(snapDir, n))
				assert.NoError(t, err)
				dst, err := os.OpenFile(path.Join(perfTestDir, fmt.Sprintf("silo_%s_%s", sConf.name, n)), os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0666)
				assert.NoError(t, err)
				_, err = io.Copy(dst, src)
				assert.NoError(t, err)
				err = src.Close()
				assert.NoError(t, err)
				err = dst.Close()
				assert.NoError(t, err)

				dev.Base = path.Join(perfTestDir, fmt.Sprintf("silo_%s_%s", sConf.name, n))
			}
			devicesFrom = append(devicesFrom, dev)
		}

		rp := &FirecrackerRuntimeProvider[struct{}, ipc.AgentServerRemote[struct{}], struct{}]{
			Log: log,
			HypervisorConfiguration: FirecrackerMachineConfig{
				FirecrackerBin: firecrackerBin,
				JailerBin:      jailerBin,
				ChrootBaseDir:  perfTestDir,
				UID:            0,
				GID:            0,
				NetNS:          netns,
				NumaNode:       0,
				CgroupVersion:  2,
				Stdout:         nil,
				Stderr:         nil,
				Stdin:          nil,
			},
			StateName:        common.DeviceStateName,
			MemoryName:       common.DeviceMemoryName,
			AgentServerLocal: struct{}{},
		}

		myPeer, err := peer.StartPeer(context.TODO(), context.Background(), log, dummyMetrics, nil, sConf.name, rp)
		assert.NoError(t, err)

		// NB: We set it here to get rid of the uuid prefix Peer adds.
		myPeer.SetInstanceID(sConf.name)

		hooks1 := peer.MigrateFromHooks{
			OnLocalDeviceRequested:     func(id uint32, path string) {},
			OnLocalDeviceExposed:       func(id uint32, path string) {},
			OnLocalAllDevicesRequested: func() {},
			OnXferCustomData:           func(data []byte) {},
		}

		err = myPeer.MigrateFrom(context.TODO(), devicesFrom, nil, nil, hooks1)
		assert.NoError(t, err)

		err = myPeer.Resume(context.TODO(), 10*time.Second, 10*time.Second)
		assert.NoError(t, err)

		runtimeStart := time.Now()

		siloSet, siloGet := bench(t, sConf.name, 3333)

		siloTimingsSet[sConf.name] = siloSet
		siloTimingsGet[sConf.name] = siloGet

		err = myPeer.Close()
		assert.NoError(t, err)

		siloTimingsRuntime[sConf.name] = time.Since(runtimeStart)
	}

	// NOW TRY WITHOUT SILO

	m, err := StartFirecrackerMachine(ctx, log, &FirecrackerMachineConfig{
		FirecrackerBin: firecrackerBin,
		JailerBin:      jailerBin,
		ChrootBaseDir:  perfTestDir,
		UID:            0,
		GID:            0,
		NetNS:          netns,
		NumaNode:       0,
		CgroupVersion:  2,
		Stdout:         nil,
		Stderr:         nil,
		Stdin:          nil,
	})
	assert.NoError(t, err)

	// Copy the devices in from the last place... (No Silo in the mix here)
	for _, d := range deviceFiles {
		src := path.Join(devicesAt, d)
		dst := path.Join(m.VMPath, d)
		os.Link(src, dst)
	}
	devicesAt = m.VMPath

	resumeSnapshotAndAcceptCtx, cancelResumeSnapshotAndAcceptCtx := context.WithTimeout(ctx, 10*time.Second)
	defer cancelResumeSnapshotAndAcceptCtx()

	err = m.ResumeSnapshot(resumeSnapshotAndAcceptCtx, common.DeviceStateName, common.DeviceMemoryName)
	assert.NoError(t, err)

	agent, err := ipc.StartAgentRPC[struct{}, ipc.AgentServerRemote[struct{}]](
		log, path.Join(m.VMPath, VSockName),
		agentVsockPort, agentLocal)
	assert.NoError(t, err)

	// Call after resume RPC
	afterResumeCtx, cancelAfterResumeCtx := context.WithTimeout(ctx, 10*time.Second)
	defer cancelAfterResumeCtx()

	r, err := agent.GetRemote(afterResumeCtx)
	assert.NoError(t, err)

	remote := *(*ipc.AgentServerRemote[struct{}])(unsafe.Pointer(&r))
	err = remote.AfterResume(afterResumeCtx)
	assert.NoError(t, err)

	nosiloStart := time.Now()

	nosiloSet, nosiloGet := bench(t, "nosilo", 3333)

	suspendCtx, cancelSuspendCtx := context.WithTimeout(ctx, 10*time.Second)
	defer cancelSuspendCtx()

	r, err = agent.GetRemote(suspendCtx)
	assert.NoError(t, err)

	remote = *(*ipc.AgentServerRemote[struct{}])(unsafe.Pointer(&r))
	err = remote.BeforeSuspend(suspendCtx)
	assert.NoError(t, err)

	err = agent.Close()
	assert.NoError(t, err)

	nosiloRuntime := time.Since(nosiloStart)

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
			size := int64(1024 * 1024 * 1024) // TODO: Dynamic
			baseFile := path.Join(snapDir, n)
			sp0, err := sources.NewFileStorage(baseFile, size)
			if err != nil {
				panic(err)
			}
			overlayFile := path.Join(path.Join(perfTestDir, conf.name, fmt.Sprintf("%s.overlay", fn)))
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

// Run some benchmark against the vm
func bench(t *testing.T, name string, port int) (time.Duration, time.Duration) {
	var err error
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

	fmt.Printf(" ### Benchmarking %s\n", name)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var client valkey.Client

	for i := 0; i < 10; i++ {
		client, err = valkey.NewClient(valkey.ClientOption{InitAddress: []string{fmt.Sprintf("127.0.0.1:%d", port)}})
		if err == nil {
			break
		}
		fmt.Printf("Error connecting. Retrying in a bit. %v\n", err)
		time.Sleep(10 * time.Second)
	}

	if client == nil {
		fmt.Printf("Error connecting to valkey!\n")
		require.NotNil(t, client)
		return 0, 0
	}

	numSet := 2000000 // Number of keys

	setErrors := 0
	// SET key val NX
	ctime := time.Now()
	for i := 0; i < numSet; i++ {
		key := fmt.Sprintf("key-%d", i)
		val := uuid.NewString()
		err = client.Do(ctx, client.B().Set().Key(key).Value(val).Nx().Build()).Error()
		if err != nil {
			setErrors++
		}
		//assert.NoError(t, err)
	}
	timeSet := time.Since(ctime)
	fmt.Printf("BENCH %s Set %d keys in %dms\n", name, numSet, timeSet.Milliseconds())

	getErrors := 0
	ctime = time.Now()
	for _, k := range rand.Perm(numSet) {
		key := fmt.Sprintf("key-%d", k)
		_, err = client.Do(ctx, client.B().Get().Key(key).Build()).ToString()
		if err != nil {
			getErrors++
		}
		//assert.NoError(t, err)
	}
	timeGet := time.Since(ctime)
	fmt.Printf("BENCH %s Get %d keys in %dms (random order) [%d set errors, %d get errors]\n",
		name, numSet, timeGet.Milliseconds(), setErrors, getErrors)

	client.Close()

	return timeSet, timeGet
}
