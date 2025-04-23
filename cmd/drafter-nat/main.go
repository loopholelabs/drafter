package main

import (
	"context"
	"flag"
	"os"
	"os/signal"

	route "github.com/nixigaj/go-default-route"

	"github.com/loopholelabs/drafter/pkg/network/nat"
	"github.com/loopholelabs/logging"
)

func main() {
	hostInterface := flag.String("host-interface", "", "Host gateway interface")
	hostVethCIDR := flag.String("host-veth-cidr", "10.0.8.0/22", "CIDR for the veths outside the namespace")
	namespaceVethCIDR := flag.String("namespace-veth-cidr", "10.0.15.0/24", "CIDR for the veths inside the namespace")
	blockedSubnetCIDR := flag.String("blocked-subnet-cidr", "10.0.15.0/24", "CIDR to block for the namespace")
	namespaceInterface := flag.String("namespace-interface", "tap0", "Name for the interface inside the namespace")
	namespaceInterfaceGateway := flag.String("namespace-interface-gateway", "172.16.0.1", "Gateway for the interface inside the namespace")
	namespaceInterfaceNetmask := flag.Uint("namespace-interface-netmask", 30, "Netmask for the interface inside the namespace")
	namespaceInterfaceIP := flag.String("namespace-interface-ip", "172.16.0.2", "IP for the interface inside the namespace")
	namespaceInterfaceMAC := flag.String("namespace-interface-mac", "02:0e:d9:fd:68:3d", "MAC address for the interface inside the namespace")
	namespacePrefix := flag.String("namespace-prefix", "ark", "Prefix for the namespace IDs")
	allowIncomingTraffic := flag.Bool("allow-incoming-traffic", true, "Whether to allow incoming traffic to the namespaces (at host-veth-internal-ip:port)")

	flag.Parse()

	log := logging.New(logging.Zerolog, "drafter", os.Stderr)

	if *hostInterface == "" {
		ifc, err := route.DefaultRouteInterface()
		if err != nil {
			panic(err)
		}
		*hostInterface = ifc.Name
		log.Info().Str("hostInterface", *hostInterface).Msg("found default route interface")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt)
	go func() {
		<-done
		log.Info().Msg("Exiting gracefully")
		cancel()
	}()

	namespaces, err := nat.CreateNAT(ctx, context.Background(),

		nat.TranslationConfiguration{
			HostInterface:             *hostInterface,
			HostVethCIDR:              *hostVethCIDR,
			NamespaceVethCIDR:         *namespaceVethCIDR,
			BlockedSubnetCIDR:         *blockedSubnetCIDR,
			NamespaceInterface:        *namespaceInterface,
			NamespaceInterfaceGateway: *namespaceInterfaceGateway,
			NamespaceInterfaceNetmask: uint32(*namespaceInterfaceNetmask),
			NamespaceInterfaceIP:      *namespaceInterfaceIP,
			NamespaceInterfaceMAC:     *namespaceInterfaceMAC,
			NamespacePrefix:           *namespacePrefix,
			AllowIncomingTraffic:      *allowIncomingTraffic,
		},

		nat.CreateNamespacesHooks{
			OnBeforeCreateNamespace: func(id string) {
				log.Info().Str("id", id).Msg("Creating namespace")
			},
			OnBeforeRemoveNamespace: func(id string) {
				log.Info().Str("id", id).Msg("Removing namespace")
			},
		},
		256,
	)

	if err != nil {
		log.Error().Err(err).Msg("Error creating namespaces")
		panic(err)
	}

	log.Info().Msg("Created namespaces")

	// Wait for cancel
	<-ctx.Done()

	err = namespaces.Close()
	if err != nil {
		log.Error().Err(err).Msg("Error closing namespaces")
	}

	log.Info().Msg("Shutting down")
}
