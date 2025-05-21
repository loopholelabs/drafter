package main

import (
	"context"
	"encoding/json"
	"flag"
	"net"
	"os"
	"os/signal"

	"github.com/loopholelabs/drafter/pkg/network/forwarder"
	"github.com/loopholelabs/logging"
)

func main() {
	rawHostVethCIDR := flag.String("host-veth-cidr", "10.0.8.0/22", "CIDR for the veths outside the namespace")

	defaultPortForwards, err := json.Marshal([]forwarder.PortForward{
		{
			Netns:        "ark0",
			InternalPort: "6379",
			Protocol:     "tcp",
			ExternalAddr: "127.0.0.1:3333",
		},
	})
	if err != nil {
		panic(err)
	}

	rawPortForwards := flag.String("port-forwards", string(defaultPortForwards), "Port forwards configuration (wildcard IPs like 0.0.0.0 are not valid, be explicit)")

	flag.Parse()

	log := logging.New(logging.Zerolog, "drafter", os.Stderr)

	var portForwards []forwarder.PortForward
	err = json.Unmarshal([]byte(*rawPortForwards), &portForwards)
	if err != nil {
		log.Error().Err(err).Msg("Error in portForwards")
		panic(err)
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

	_, hostVethCIDR, err := net.ParseCIDR(*rawHostVethCIDR)
	if err != nil {
		log.Error().Err(err).Msg("could not parse CIDR")
		panic(err)
	}

	forwardedPorts, err := forwarder.ForwardPorts(
		ctx,
		hostVethCIDR,
		portForwards,

		forwarder.PortForwardHooks{
			OnAfterPortForward: func(portID int, netns, internalIP, internalPort, externalIP, externalPort, protocol string) {
				log.Info().
					Int("portID", portID).
					Str("netns", netns).
					Str("internalIP", internalIP).
					Str("internalPort", internalPort).
					Str("externalIP", externalIP).
					Str("externalPort", externalPort).
					Str("protocol", protocol).
					Msg("Forwarding port")
			},
			OnBeforePortUnforward: func(portID int) {
				log.Info().
					Int("portID", portID).
					Msg("Unforwarding port")
			},
		},
	)

	if err != nil {
		log.Error().Err(err).Msg("Error forwarding port")
		panic(err)
	}

	log.Info().Msg("Forwarded all configured ports")

	<-ctx.Done()

	err = forwardedPorts.Close()
	if err != nil {
		log.Error().Err(err).Msg("Error closing forwardedPorts")
	}

	log.Info().Msg("Shutting down")
}
