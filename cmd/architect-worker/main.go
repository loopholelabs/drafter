package main

import (
	"flag"
	"fmt"
	"sync"

	"github.com/loopholelabs/architekt/pkg/firecracker"
	"github.com/loopholelabs/architekt/pkg/network"
)

func main() {
	firecrackerBin := flag.String("firecracker-bin", "firecracker", "Firecracker binary")

	verbose := flag.Bool("verbose", false, "Whether to enable verbose logging")
	enableOutput := flag.Bool("enable-output", true, "Whether to enable VM stdout and stderr")
	enableInput := flag.Bool("enable-input", false, "Whether to enable VM stdin")

	hostInterface := flag.String("host-interface", "vm0", "Host interface name")
	hostMAC := flag.String("host-mac", "02:0e:d9:fd:68:3d", "Host MAC address")
	bridgeInterface := flag.String("bridge-interface", "firecracker0", "Bridge interface name")

	flag.Parse()

	tap := network.NewTAP(
		*hostInterface,
		*hostMAC,
		*bridgeInterface,
	)

	if err := tap.Open(); err != nil {
		panic(err)
	}
	defer tap.Close()

	srv := firecracker.NewServer(
		*firecrackerBin,

		*verbose,
		*enableOutput,
		*enableInput,
	)

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()

		if err := srv.Wait(); err != nil {
			panic(err)
		}
	}()

	socket, err := srv.Start()
	if err != nil {
		panic(err)
	}
	defer srv.Stop()

	fmt.Println(socket)

	wg.Wait()
}
