package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net"
	"os"
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

	firecrackerBin := flag.String("firecracker-bin", filepath.Join("/usr", "local", "bin", "firecracker"), "Firecracker binary")
	jailerBin := flag.String("jailer-bin", filepath.Join("/usr", "local", "bin", "jailer"), "Jailer binary (from Firecracker)")

	chrootBaseDir := flag.String("chroot-base-dir", filepath.Join("out", "vms"), "`chroot` base directory")

	uid := flag.Int("uid", 0, "User ID for the Firecracker process")
	gid := flag.Int("gid", 0, "Group ID for the Firecracker process")

	enableOutput := flag.Bool("enable-output", true, "Whether to enable VM stdout and stderr")
	enableInput := flag.Bool("enable-input", false, "Whether to enable VM stdin")

	numaNode := flag.Int("numa-node", 0, "NUMA node to run Firecracker in")
	cgroupVersion := flag.Int("cgroup-version", 2, "Cgroup version to use for Jailer")

	lhost := flag.String("lhost", "localhost", "Hostname or IP to listen on")
	ahost := flag.String("ahost", "localhost", "Hostname or IP to advertise")

	verbose := flag.Bool("verbose", false, "Whether to enable verbose logging")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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

		func() (string, error) {
			// TODO: Claim namespace prefix here
			return *namespacePrefix + "0", nil
		},
		func(namespace string) error {
			// TODO: Release namespace prefix here
			return nil
		},

		services.HypervisorConfiguration{
			FirecrackerBin: *firecrackerBin,
			JailerBin:      *jailerBin,

			ChrootBaseDir: *chrootBaseDir,

			UID: *uid,
			GID: *gid,

			NumaNode:      *numaNode,
			CgroupVersion: *cgroupVersion,

			EnableOutput: *enableOutput,
			EnableInput:  *enableInput,
		},

		*lhost,
		*ahost,
	)

	clients := 0

	registry := rpc.NewRegistry(
		svc,
		struct{}{},

		time.Second*10,
		ctx,
		&rpc.Options{
			OnClientConnect: func(remoteID string) {
				clients++

				log.Printf("%v clients connected to worker", clients)
			},
			OnClientDisconnect: func(remoteID string) {
				clients--

				log.Printf("%v clients connected to worker", clients)
			},
		},
	)

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

	go func() {
		if err := registry.LinkStream(
			json.NewEncoder(conn).Encode,
			json.NewDecoder(conn).Decode,

			json.Marshal,
			json.Unmarshal,
		); err != nil && !utils.IsClosedErr(err) {
			panic(err)
		}
	}()

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt)

	<-done

	log.Println("Exiting gracefully")

	_ = services.CloseWorker(svc)

	if err := daemon.Close(ctx); err != nil {
		panic(err)
	}
}
