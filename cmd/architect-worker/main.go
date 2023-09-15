package main

import (
	"flag"
	"fmt"
	"sync"

	"github.com/loopholelabs/architekt/pkg/firecracker"
)

func main() {
	firecrackerBin := flag.String("firecracker-bin", "firecracker", "Firecracker binary")
	verbose := flag.Bool("verbose", false, "Whether to enable verbose logging")
	enableOutput := flag.Bool("enable-output", true, "Whether to enable VM stdout and stderr")
	enableInput := flag.Bool("enable-input", false, "Whether to enable VM stdin")

	flag.Parse()

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
