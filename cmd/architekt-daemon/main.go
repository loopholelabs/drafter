package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"

	"github.com/loopholelabs/architekt/pkg/network"
)

var (
	errNotEnoughAvailableIPsInHostCIDR      = errors.New("not enough available IPs in host CIDR")
	errNotEnoughAvailableIPsInNamespaceCIDR = errors.New("not enough available IPs in namespace CIDR")
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

	if err := network.CreateNAT(*hostInterface); err != nil {
		panic(err)
	}

	defer network.RemoveNAT(*hostInterface)

	hostVethIPs := network.NewIPTable(*hostVethCIDR)
	if err := hostVethIPs.Open(ctx); err != nil {
		panic(err)
	}

	namespaceVethIPs := network.NewIPTable(*namespaceVethCIDR)
	if err := namespaceVethIPs.Open(ctx); err != nil {
		panic(err)
	}

	if namespaceVethIPs.AvailableIPs() > hostVethIPs.AvailablePairs() {
		panic(errNotEnoughAvailableIPsInHostCIDR)
	}

	availableIPs := namespaceVethIPs.AvailableIPs()
	if availableIPs < 1 {
		panic(errNotEnoughAvailableIPsInNamespaceCIDR)
	}

	var (
		namespaceVeths     = []*network.IP{}
		namespaceVethsLock sync.Mutex

		namespaces     = []*network.Namespace{}
		namespacesLock sync.Mutex

		setupWg sync.WaitGroup
	)

	for i := uint64(0); i < availableIPs; i++ {
		setupWg.Add(1)

		go func(i uint64) {
			defer setupWg.Done()

			id := fmt.Sprintf("%v%v", *namespacePrefix, i)

			if *verbose {
				log.Println("Creating namespace", id)
			}

			hostVeth, err := hostVethIPs.GetPair(ctx)
			if err != nil {
				panic(err)
			}

			namespaceVeth, err := namespaceVethIPs.GetIP(ctx)
			if err != nil {
				panic(err)
			}

			namespaceVethsLock.Lock()
			namespaceVeths = append(namespaceVeths, namespaceVeth)
			namespaceVethsLock.Unlock()

			namespace := network.NewNamespace(
				id,

				*hostInterface,
				*namespaceInterface,

				*namespaceInterfaceGateway,
				uint32(*namespaceInterfaceNetmask),

				hostVeth.GetFirstIP().String(),
				hostVeth.GetSecondIP().String(),

				*namespaceInterfaceIP,
				namespaceVeth.String(),

				*namespaceVethCIDR,

				*namespaceInterfaceMAC,
			)
			if err := namespace.Open(); err != nil {
				panic(err)
			}

			namespacesLock.Lock()
			namespaces = append(namespaces, namespace)
			namespacesLock.Unlock()
		}(i)
	}

	setupWg.Wait()

	var teardownWg sync.WaitGroup
	defer func() {
		for _, namespace := range namespaces {
			teardownWg.Add(1)

			go func(ns *network.Namespace) {
				defer teardownWg.Done()

				if *verbose {
					log.Println("Removing namespace")
				}

				if err := ns.Close(); err != nil {
					panic(err)
				}
			}(namespace)
		}

		teardownWg.Wait()
	}()

	defer func() {
		for _, namespaceVeth := range namespaceVeths {
			if err := namespaceVethIPs.ReleaseIP(ctx, namespaceVeth); err != nil {
				panic(err)
			}
		}
	}()

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt)

	log.Println("Network namespaces ready")

	<-done

	log.Println("Exiting gracefully")
}
