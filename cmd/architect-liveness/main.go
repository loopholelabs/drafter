package main

import (
	"flag"

	"github.com/loopholelabs/architekt/pkg/liveness"
)

func main() {
	vsockCID := flag.Int("vsock-cid", 2, "VSock CID")
	vsockPort := flag.Int("vsock-port", 25, "VSock port")

	flag.Parse()

	if err := liveness.SendLivenessPing(uint32(*vsockCID), uint32(*vsockPort)); err != nil {
		panic(err)
	}
}
