package main

import (
	"flag"
	"fmt"
	"os/exec"
	"sync"

	"github.com/loopholelabs/architekt/pkg/firecracker"
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

	if output, err := exec.Command("ip", "tuntap", "add", *hostInterface, "mode", "tap").CombinedOutput(); err != nil {
		panic(string(output))
	}

	if output, err := exec.Command("ip", "link", "set", "dev", *hostInterface, "master", *bridgeInterface).CombinedOutput(); err != nil {
		panic(string(output))
	}

	if output, err := exec.Command("ip", "link", "set", "dev", *hostInterface, "address", *hostMAC).CombinedOutput(); err != nil {
		panic(string(output))
	}

	if output, err := exec.Command("ip", "link", "set", *hostInterface, "up").CombinedOutput(); err != nil {
		panic(string(output))
	}

	if output, err := exec.Command("sysctl", "-w", "net.ipv4.conf."+*hostInterface+".proxy_arp=1").CombinedOutput(); err != nil {
		panic(string(output))
	}

	defer func() {
		if output, err := exec.Command("ip", "tuntap", "del", "dev", *hostInterface, "mode", "tap").CombinedOutput(); err != nil {
			panic(string(output))
		}
	}()

	instance := firecracker.NewFirecrackerInstance(
		*firecrackerBin,

		*verbose,
		*enableOutput,
		*enableInput,
	)

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()

		if err := instance.Wait(); err != nil {
			panic(err)
		}
	}()

	socket, err := instance.Start()
	if err != nil {
		panic(err)
	}
	defer instance.Stop()

	fmt.Println(socket)

	wg.Wait()
}
