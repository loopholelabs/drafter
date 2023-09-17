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

	statePath := flag.String("state-path", "out/image/architekt.state", "State path")
	memoryPath := flag.String("memory-path", "out/image/architekt.memory", "Memory path")

	stop := flag.Bool("stop", false, "Whether to stop the VM")
	resumeSnapshot := flag.Bool("resume-snapshot", false, "Whether to resume a VM snapshot")
	flushSnapshot := flag.Bool("flush-snapshot", false, "Whether to flush a VM snapshot")

	flag.Parse()

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.Dial("unix", *firecrackerSocketPath)
			},
		},
	}

	if *stop {
		if err := firecracker.StopVM(client); err != nil {
			panic(err)
		}
	}

	if *resumeSnapshot {
		if err := firecracker.ResumeSnapshot(
			client,

			*statePath,
			*memoryPath,
		); err != nil {
			panic(err)
		}
	}

	if *flushSnapshot {
		if err := firecracker.FlushSnapshot(
			client,

			*statePath,
		); err != nil {
			panic(err)
		}
	}
}
