package main

import (
	"flag"

	"github.com/loopholelabs/drafter/pkg/vsock"
)

func main() {
	vsockPort := flag.Int("vsock-port", 25, "VSock port")

	flag.Parse()

	if err := vsock.SendLivenessPing(vsock.CIDHost, uint32(*vsockPort)); err != nil {
		panic(err)
	}
}
