package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"

	"github.com/loopholelabs/drafter/pkg/roles"
	"github.com/loopholelabs/drafter/pkg/utils"
)

func main() {
	hostInterface := flag.String("host-interface", "wlp0s20f3", "Host gateway interface")

	hostVethCIDR := flag.String("host-veth-cidr", "10.0.8.0/22", "CIDR for the veths outside the namespace")
	namespaceVethCIDR := flag.String("namespace-veth-cidr", "10.0.15.0/24", "CIDR for the veths inside the namespace")
	blockedSubnetCIDR := flag.String("blocked-subnet-cidr", "10.0.15.0/24", "CIDR to block for the namespace")

	namespaceInterface := flag.String("namespace-interface", "tap0", "Name for the interface inside the namespace")
	namespaceInterfaceGateway := flag.String("namespace-interface-gateway", "172.100.100.1", "Gateway for the interface inside the namespace")
	namespaceInterfaceNetmask := flag.Uint("namespace-interface-netmask", 30, "Netmask for the interface inside the namespace")
	namespaceInterfaceIP := flag.String("namespace-interface-ip", "172.100.100.2", "IP for the interface inside the namespace")
	namespaceInterfaceMAC := flag.String("namespace-interface-mac", "02:0e:d9:fd:68:3d", "MAC address for the interface inside the namespace")

	namespacePrefix := flag.String("namespace-prefix", "ark", "Prefix for the namespace IDs")

	allowIncomingTraffic := flag.Bool("allow-incoming-traffic", true, "Whether to allow incoming traffic to the namespaces (at host-veth-internal-ip:port)")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var errs error
	defer func() {
		if errs != nil {
			panic(errs)
		}
	}()

	ctx, handlePanics, handleGoroutinePanics, cancel, wait, _ := utils.GetPanicHandler(
		ctx,
		&errs,
		utils.GetPanicHandlerHooks{},
	)
	defer wait()
	defer cancel()
	defer handlePanics(false)()

	go func() {
		done := make(chan os.Signal, 1)
		signal.Notify(done, os.Interrupt)

		<-done

		log.Println("Exiting gracefully")

		cancel()
	}()

	nat, err := roles.CreateNAT(
		ctx,
		context.Background(), // Never give up on rescue operations

		*hostInterface,

		*hostVethCIDR,
		*namespaceVethCIDR,
		*blockedSubnetCIDR,

		*namespaceInterface,
		*namespaceInterfaceGateway,
		uint32(*namespaceInterfaceNetmask),
		*namespaceInterfaceIP,
		*namespaceInterfaceMAC,

		*namespacePrefix,

		*allowIncomingTraffic,

		roles.CreateNamespacesHooks{
			OnBeforeCreateNamespace: func(id string) {
				log.Println("Creating namespace", id)
			},
			OnBeforeRemoveNamespace: func(id string) {
				log.Println("Removing namespace", id)
			},
		},
	)

	if nat.Wait != nil {
		defer func() {
			defer handlePanics(true)()

			if err := nat.Wait(); err != nil {
				panic(err)
			}
		}()
	}

	if err != nil {
		panic(err)
	}

	defer func() {
		defer handlePanics(true)()

		if err := nat.Close(); err != nil {
			panic(err)
		}
	}()

	handleGoroutinePanics(true, func() {
		if err := nat.Wait(); err != nil {
			panic(err)
		}
	})

	log.Println("Created all namespaces")

	<-ctx.Done()

	log.Println("Shutting down")
}
