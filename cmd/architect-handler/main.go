package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/loopholelabs/architekt/pkg/services"
	"github.com/loopholelabs/architekt/pkg/utils"
	"github.com/pojntfx/dudirekta/pkg/rpc"
)

var (
	errCouldNotConnectToVSock = errors.New("could not connect to VSock")
	errPeerNotFound           = errors.New("peer not found")
)

func main() {
	vsockPath := flag.String("vsock-path", filepath.Join("out", "package", "vsock.sock"), "VSock path")
	agentVSockPort := flag.Int("agent-vsock-port", 26, "Agent VSock port")

	beforeSuspend := flag.Bool("before-suspend", false, "Whether to run the BeforeSuspend handler")
	afterResume := flag.Bool("after-resume", false, "Whether to run the AfterResume handler")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ready := make(chan string)

	clients := 0
	registry := rpc.NewRegistry(
		struct{}{},
		services.AgentRemote{},

		time.Second*10,
		ctx,
		&rpc.Options{
			ResponseBufferLen: rpc.DefaultResponseBufferLen,
			OnClientConnect: func(remoteID string) {
				clients++

				log.Printf("%v clients connected", clients)

				ready <- remoteID
			},
			OnClientDisconnect: func(remoteID string) {
				clients--

				log.Printf("%v clients connected", clients)
			},
		},
	)

	conn, err := net.Dial("unix", *vsockPath)
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte(fmt.Sprintf("CONNECT %d\n", *agentVSockPort))); err != nil {
		panic(err)
	}

	line, err := utils.ReadLineNoBuffer(conn)
	if err != nil {
		panic(err)
	}

	if !strings.HasPrefix(line, "OK ") {
		panic(errCouldNotConnectToVSock)
	}

	log.Println("Connected to", conn.RemoteAddr())

	var wg sync.WaitGroup
	defer wg.Wait()

	wg.Add(1)
	go func() {
		defer wg.Done()

		if err := registry.Link(conn); err != nil && !strings.HasSuffix(err.Error(), "use of closed network connection") {
			panic(err)
		}
	}()

	remoteID := <-ready
	peer, ok := registry.Peers()[remoteID]
	if !ok {
		panic(errPeerNotFound)
	}

	if *beforeSuspend {
		if err := peer.BeforeSuspend(ctx); err != nil {
			panic(err)
		}
	}

	if *afterResume {
		if err := peer.AfterResume(ctx); err != nil {
			panic(err)
		}
	}

	if err := conn.Close(); err != nil {
		panic(err)
	}
}
