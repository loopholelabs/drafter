package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"

	"github.com/loopholelabs/drafter/pkg/roles"
	"github.com/loopholelabs/drafter/pkg/utils"
)

func main() {
	rawHostVethCIDR := flag.String("host-veth-cidr", "10.0.8.0/22", "CIDR for the veths outside the namespace")

	defaultPortForwards, err := json.Marshal([]roles.PortForward{
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

	var portForwards []roles.PortForward
	if err := json.Unmarshal([]byte(*rawPortForwards), &portForwards); err != nil {
		panic(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var errs error
	defer func() {
		if errs != nil {
			panic(errs)
		}
	}()

	panicHandler := utils.NewPanicHandler(
		ctx,
		&errs,
		utils.GetPanicHandlerHooks{},
	)
	defer panicHandler.Wait()
	defer panicHandler.Cancel()
	defer panicHandler.HandlePanics(false)()

	go func() {
		done := make(chan os.Signal, 1)
		signal.Notify(done, os.Interrupt)

		<-done

		log.Println("Exiting gracefully")

		cancel()
	}()

	_, hostVethCIDR, err := net.ParseCIDR(*rawHostVethCIDR)
	if err != nil {
		panic(err)
	}

	forwardedPorts, err := roles.ForwardPorts(
		panicHandler.InternalCtx,

		hostVethCIDR,
		portForwards,

		roles.PortForwardHooks{
			OnAfterPortForward: func(portID int, netns, internalIP, internalPort, externalIP, externalPort, protocol string) {
				log.Printf("Forwarding port with ID %v from %v:%v/%v in network namespace %v to %v:%v/%v", portID, internalIP, internalPort, protocol, netns, externalIP, externalPort, protocol)
			},
			OnBeforePortUnforward: func(portID int) {
				log.Println("Unforwarding port with ID", portID)
			},
		},
	)

	if forwardedPorts.Wait != nil {
		defer func() {
			defer panicHandler.HandlePanics(true)()

			if err := forwardedPorts.Wait(); err != nil {
				panic(err)
			}
		}()
	}

	if err != nil {
		panic(err)
	}

	defer func() {
		defer panicHandler.HandlePanics(true)()

		if err := forwardedPorts.Close(); err != nil {
			panic(err)
		}
	}()

	panicHandler.HandleGoroutinePanics(true, func() {
		if err := forwardedPorts.Wait(); err != nil {
			panic(err)
		}
	})

	log.Println("Forwarded all configured ports")

	<-panicHandler.InternalCtx.Done()

	log.Println("Shutting down")
}
