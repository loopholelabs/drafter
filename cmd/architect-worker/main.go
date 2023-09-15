package main

import (
	"flag"
	"fmt"

	"github.com/loopholelabs/architekt/pkg/firecracker"
)

func main() {
	firecrackerBin := flag.String("firecracker-bin", "firecracker", "Firecracker binary")
	verbose := flag.Bool("verbose", false, "Whether to enable verbose logging")

	flag.Parse()

	instance := firecracker.NewFirecrackerInstance(*firecrackerBin, *verbose)

	go func() {
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

	select {}
}
