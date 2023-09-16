package main

import (
	"flag"
	"log"

	"github.com/mdlayher/vsock"
)

func main() {
	vsockCID := flag.Int("vsock-cid", 2, "VSock CID")
	vsockPort := flag.Int("vsock-port", 25, "VSock port")

	flag.Parse()

	conn, err := vsock.Dial(uint32(*vsockCID), uint32(*vsockPort), nil)
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	log.Println("Connected to", conn.RemoteAddr())
}
