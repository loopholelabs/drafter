package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"

	"github.com/loopholelabs/drafter/pkg/forwarder"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
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

	var portForwards []forwarder.PortForward
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

	goroutineManager := manager.NewGoroutineManager(
		ctx,
		&errs,
		manager.GoroutineManagerHooks{},
	)
	defer goroutineManager.Wait()
	defer goroutineManager.StopAllGoroutines()
	defer goroutineManager.CreateBackgroundPanicCollector()()

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

	forwardedPorts, err := forwarder.ForwardPorts(
		goroutineManager.Context(),

		hostVethCIDR,
		portForwards,

		forwarder.PortForwardHooks{
			OnAfterPortForward: func(portID int, netns, internalIP, internalPort, externalIP, externalPort, protocol string) {
				log.Printf("Forwarding port with ID %v from %v:%v/%v in network namespace %v to %v:%v/%v", portID, internalIP, internalPort, protocol, netns, externalIP, externalPort, protocol)
			},
			OnBeforePortUnforward: func(portID int) {
				log.Println("Unforwarding port with ID", portID)
			},
		},
	)

	defer func() {
		defer goroutineManager.CreateForegroundPanicCollector()()

		if err := forwardedPorts.Wait(); err != nil {
			panic(err)
		}
	}()

	if err != nil {
		panic(err)
	}

	defer func() {
		defer goroutineManager.CreateForegroundPanicCollector()()

		if err := forwardedPorts.Close(); err != nil {
			panic(err)
		}
	}()

	goroutineManager.StartForegroundGoroutine(func(_ context.Context) {
		if err := forwardedPorts.Wait(); err != nil {
			panic(err)
		}
	})

	log.Println("Forwarded all configured ports")

	<-goroutineManager.Context().Done()

	log.Println("Shutting down")
}
