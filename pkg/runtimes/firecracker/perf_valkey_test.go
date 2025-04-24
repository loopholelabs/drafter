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

	"github.com/stretchr/testify/assert"
	valkey "github.com/valkey-io/valkey-go"
)

const perfTestDir = "perf_test"

/**
 * This creates a snapshot, and then runs some perf testing
 *
 * firecracker needs to work
 * blueprints expected to exist at ./out/blueprint
 *
 */
func TestValkeyPerf(t *testing.T) {
	/*
		f, err := os.Create("valkey_perf_cpu_profile")
		if err != nil {
			panic(err)
		}
		pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	*/
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

	var siloGetNoCow time.Duration
	var siloSetNoCow time.Duration
	var siloGet time.Duration
	var siloSet time.Duration

	dummyMetrics := testutil.NewDummyMetrics()

	// FIXME: Doesn't work atm to do both... they stomp on each other somehow
	for _, useCow := range []bool{false, true} {

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
			}

			if useCow {
				dev.Base = path.Join(snapDir, n)
				dev.Overlay = path.Join(path.Join(perfTestDir, "silo", fmt.Sprintf("%s.overlay", fn)))
				dev.State = path.Join(path.Join(perfTestDir, "silo", fmt.Sprintf("%s.state", fn)))
			} else {
				// Copy the file
				src, err := os.Open(path.Join(snapDir, n))
				assert.NoError(t, err)
				dst, err := os.OpenFile(path.Join(perfTestDir, fmt.Sprintf("silo_%s", n)), os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0666)
				assert.NoError(t, err)
				_, err = io.Copy(dst, src)
				assert.NoError(t, err)
				err = src.Close()
				assert.NoError(t, err)
				err = dst.Close()
				assert.NoError(t, err)

				dev.Base = path.Join(perfTestDir, fmt.Sprintf("silo_%s", n))
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
				EnableInput:    false,
			},
			StateName:        common.DeviceStateName,
			MemoryName:       common.DeviceMemoryName,
			AgentServerLocal: struct{}{},
		}

		id := "testNoCow"
		if useCow {
			id = "test"
		}
		myPeer, err := peer.StartPeer(context.TODO(), context.Background(), log, dummyMetrics, nil, id, rp)
		assert.NoError(t, err)

		// NB: We set it here to get rid of the uuid prefix Peer adds.
		myPeer.SetInstanceID(id)

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

		if useCow {
			siloSet, siloGet = bench(t, "silo", 3333)
		} else {
			siloSetNoCow, siloGetNoCow = bench(t, "silo_no_cow", 3333)
		}

		err = myPeer.Close()
		assert.NoError(t, err)
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
		EnableInput:    false,
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

	// Work out rough overhead here...

	overheadSet := (siloSet - nosiloSet) * 100 / nosiloSet
	overheadGet := (siloGet - nosiloGet) * 100 / nosiloGet

	fmt.Printf("# Silo overhead # Set %d%% Get %d%%\n", overheadSet, overheadGet)

	overheadSetNoCow := (siloSetNoCow - nosiloSet) * 100 / nosiloSet
	overheadGetNoCow := (siloGetNoCow - nosiloGet) * 100 / nosiloGet

	fmt.Printf("# Silo_no_cow overhead # Set %d%% Get %d%%\n", overheadSetNoCow, overheadGetNoCow)

	// Work out some silo metrics
	fmt.Printf("NoCow metrics\n")
	for _, r := range []string{"disk", "oci", "memory"} {
		m := dummyMetrics.GetMetrics("testNoCow", r)
		m.ShowStats(r)
	}

	fmt.Printf("Cow metrics\n")
	for _, r := range []string{"disk", "oci", "memory"} {
		m := dummyMetrics.GetMetrics("test", r)
		m.ShowStats(r)
	}

}

// Run some benchmark against the vm
func bench(t *testing.T, name string, port int) (time.Duration, time.Duration) {
	fmt.Printf(" ### Benchmarking %s\n", name)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var client valkey.Client
	var err error

	for i := 0; i < 5; i++ {
		client, err = valkey.NewClient(valkey.ClientOption{InitAddress: []string{fmt.Sprintf("127.0.0.1:%d", port)}})
		if err == nil {
			break
		}
		fmt.Printf("Error connecting. Retrying in 5 seconds. %v\n", err)
		time.Sleep(5 * time.Second)
	}

	numSet := 1000000 // Number of keys

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
