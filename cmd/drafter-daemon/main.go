package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"

	"github.com/loopholelabs/drafter/pkg/network"
)

func main() {
	hostInterface := flag.String("host-interface", "wlp0s20f3", "Host gateway interface")

	hostVethCIDR := flag.String("host-veth-cidr", "10.0.8.0/22", "CIDR for the veths outside the namespace")
	namespaceVethCIDR := flag.String("namespace-veth-cidr", "10.0.15.0/24", "CIDR for the veths inside the namespace")

	namespaceInterface := flag.String("namespace-interface", "tap0", "Name for the interface inside the namespace")
	namespaceInterfaceGateway := flag.String("namespace-interface-gateway", "172.100.100.1", "Gateway for the interface inside the namespace")
	namespaceInterfaceNetmask := flag.Uint("namespace-interface-netmask", 30, "Netmask for the interface inside the namespace")
	namespaceInterfaceIP := flag.String("namespace-interface-ip", "172.100.100.2", "IP for the interface inside the namespace")
	namespaceInterfaceMAC := flag.String("namespace-interface-mac", "02:0e:d9:fd:68:3d", "MAC address for the interface inside the namespace")

	namespacePrefix := flag.String("namespace-prefix", "ark", "Prefix for the namespace IDs")

	verbose := flag.Bool("verbose", true, "Whether to enable verbose logging")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	daemon := network.NewDaemon(
		ctx,

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

	if err := daemon.Open(ctx); err != nil {
		panic(err)
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt)

	log.Println("Network namespaces ready")

	<-done

	log.Println("Exiting gracefully")

	if err := daemon.Close(ctx); err != nil {
		panic(err)
	}
}
