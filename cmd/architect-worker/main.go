package main

import (
	"flag"
	"fmt"

	"github.com/loopholelabs/architekt/pkg/firecracker"
)

func main() {
	firecrackerBin := flag.String("firecracker-bin", "firecracker", "Firecracker binary")
	firecrackerLogLevel := flag.String("firecracker-log-level", "Warning", "Firecracker log level")
	firecrackerLogPath := flag.String("firecracker-log-path", "/dev/stderr", "Firecracker log path")

	flag.Parse()

	instance := firecracker.NewFirecrackerInstance(*firecrackerBin, *firecrackerLogLevel, *firecrackerLogPath)

	go func() {
		if err := instance.Wait(); err != nil {
			panic(err)
		}
	}()

	socket, err := instance.Start()
	if err != nil {
		panic(err)
	}

	fmt.Println(socket)

	if err := instance.Stop(); err != nil {
		panic(err)
	}
}
