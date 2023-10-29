package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/loopholelabs/architekt/pkg/network"
	"github.com/loopholelabs/architekt/pkg/services"
	"github.com/loopholelabs/architekt/pkg/utils"
	"github.com/pojntfx/dudirekta/pkg/rpc"
)

func main() {
	controlPlaneRaddr := flag.String("control-plane-raddr", "localhost:1399", "Remote address for control plane")

	hostInterface := flag.String("host-interface", "wlp0s20f3", "Host gateway interface")

	hostVethCIDR := flag.String("host-veth-cidr", "10.0.8.0/22", "CIDR for the veths outside the namespace")
	namespaceVethCIDR := flag.String("namespace-veth-cidr", "10.0.15.0/24", "CIDR for the veths inside the namespace")

	namespaceInterface := flag.String("namespace-interface", "tap0", "Name for the interface inside the namespace")
	namespaceInterfaceGateway := flag.String("namespace-interface-gateway", "172.100.100.1", "Gateway for the interface inside the namespace")
	namespaceInterfaceNetmask := flag.Uint("namespace-interface-netmask", 30, "Netmask for the interface inside the namespace")
	namespaceInterfaceIP := flag.String("namespace-interface-ip", "172.100.100.2", "IP for the interface inside the namespace")
	namespaceInterfaceMAC := flag.String("namespace-interface-mac", "02:0e:d9:fd:68:3d", "MAC address for the interface inside the namespace")

	namespacePrefix := flag.String("namespace-prefix", "ark", "Prefix for the namespace IDs")

	rawFirecrackerBin := flag.String("firecracker-bin", "firecracker", "Firecracker binary")
	rawJailerBin := flag.String("jailer-bin", "jailer", "Jailer binary (from Firecracker)")

	chrootBaseDir := flag.String("chroot-base-dir", filepath.Join("out", "vms"), "`chroot` base directory")
	cacheBaseDir := flag.String("cache-base-dir", filepath.Join("out", "cache"), "Cache base directory")

	uid := flag.Int("uid", 0, "User ID for the Firecracker process")
	gid := flag.Int("gid", 0, "Group ID for the Firecracker process")

	enableOutput := flag.Bool("enable-output", true, "Whether to enable VM stdout and stderr")
	enableInput := flag.Bool("enable-input", false, "Whether to enable VM stdin")

	numaNode := flag.Int("numa-node", 0, "NUMA node to run Firecracker in")
	cgroupVersion := flag.Int("cgroup-version", 2, "Cgroup version to use for Jailer")

	lhost := flag.String("lhost", "", "Hostname or IP to listen on")
	ahost := flag.String("ahost", "localhost", "Hostname or IP to advertise")

	name := flag.String("name", "node-1", "Name to register for")

	verbose := flag.Bool("verbose", false, "Whether to enable verbose logging")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errs := make(chan error)

	firecrackerBin, err := exec.LookPath(*rawFirecrackerBin)
	if err != nil {
		panic(err)
	}

	jailerBin, err := exec.LookPath(*rawJailerBin)
	if err != nil {
		panic(err)
	}

	daemon := network.NewDaemon(
		*hostInterface,

		*hostVethCIDR,
		*namespaceVethCIDR,

		*namespaceInterface,
		*namespaceInterfaceGateway,
		uint32(*namespaceInterfaceNetmask),
		*namespaceInterfaceIP,
		*namespaceInterfaceMAC,

		*namespacePrefix,

		func(id string) error {
			if *verbose {
				log.Println("Created namespace", id)
			}

			return nil
		},
		func(id string) error {
			if *verbose {
				log.Println("Removed namespace", id)
			}

			return nil
		},
	)

	svc := services.NewWorker(
		*verbose,

		daemon.ClaimNamespace,
		daemon.ReleaseNamespace,

		services.HypervisorConfiguration{
			FirecrackerBin: firecrackerBin,
			JailerBin:      jailerBin,

			ChrootBaseDir: *chrootBaseDir,

			UID: *uid,
			GID: *gid,

			NumaNode:      *numaNode,
			CgroupVersion: *cgroupVersion,

			EnableOutput: *enableOutput,
			EnableInput:  *enableInput,
		},

		*cacheBaseDir,

		*lhost,
		*ahost,
	)

	clients := 0

	registry := rpc.NewRegistry[services.ManagerRemote, json.RawMessage](
		svc,

		time.Second*10,
		ctx,
		&rpc.Options{
			OnClientConnect: func(remoteID string) {
				clients++

				log.Printf("%v clients connected to worker", clients)

				go func() {
					if err := services.WorkerRegister(svc, ctx, *name); err != nil {
						errs <- err

						return
					}
				}()
			},
			OnClientDisconnect: func(remoteID string) {
				clients--

				log.Printf("%v clients connected to worker", clients)
			},
		},
	)
	svc.ForRemotes = registry.ForRemotes

	if err := daemon.Open(ctx); err != nil {
		panic(err)
	}
	defer daemon.Close(ctx)

	log.Println("Network namespaces ready")

	conn, err := net.Dial("tcp", *controlPlaneRaddr)
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	log.Println("Connected to manager", conn.RemoteAddr())

	encoder := json.NewEncoder(conn)
	decoder := json.NewDecoder(conn)

	go func() {
		if err := registry.LinkStream(
			func(v rpc.Message[json.RawMessage]) error {
				return encoder.Encode(v)
			},
			func(v *rpc.Message[json.RawMessage]) error {
				return decoder.Decode(v)
			},

			func(v any) (json.RawMessage, error) {
				b, err := json.Marshal(v)
				if err != nil {
					return nil, err
				}

				return json.RawMessage(b), nil
			},
			func(data json.RawMessage, v any) error {
				return json.Unmarshal([]byte(data), v)
			},
		); err != nil && !utils.IsClosedErr(err) {
			errs <- err

			return
		}
	}()

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt)

l:
	for {
		select {
		case <-done:
			break l

		case err := <-errs:
			if err != nil {
				panic(err)
			}
		}
	}

	log.Println("Exiting gracefully")

	_ = services.CloseWorker(svc)

	if err := daemon.Close(ctx); err != nil {
		panic(err)
	}
}
