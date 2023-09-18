package main

import (
	"flag"

	"github.com/loopholelabs/architekt/pkg/firecracker"
	"github.com/loopholelabs/architekt/pkg/liveness"
)

func main() {
	vsockPort := flag.Int("vsock-port", 25, "VSock port")

	flag.Parse()

	if err := liveness.SendLivenessPing(firecracker.CIDHost, uint32(*vsockPort)); err != nil {
		panic(err)
	}
}
