package main

import (
	"context"
	"flag"
	"net"
	"net/http"

	"github.com/loopholelabs/architekt/pkg/firecracker"
)

func main() {
	firecrackerSocketPath := flag.String("firecracker-socket-path", "firecracker.sock", "Firecracker socket path")

	start := flag.Bool("start", false, "Whether to start the VM")
	stop := flag.Bool("stop", false, "Whether to stop the VM")
	flush := flag.Bool("flush", false, "Whether to flush a VM to disk (will pause the VM)")

	flag.Parse()

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.Dial("unix", *firecrackerSocketPath)
			},
		},
	}

	if *start {
		if err := firecracker.ResumeSnapshot(client); err != nil {
			panic(err)
		}
	}

	if *stop {
		if err := firecracker.StopVM(client); err != nil {
			panic(err)
		}
	}

	if *flush {
		if err := firecracker.FlushSnapshot(client); err != nil {
			panic(err)
		}
	}
}
