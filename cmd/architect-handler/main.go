package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
)

func main() {
	pwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	vsockPath := flag.String("vsock-path", filepath.Join(pwd, "vsock.sock"), "VSock path (must be absolute; will be recreated at this place when restoring)")
	vsockPort := flag.Int("vsock-port", 25, "VSock port")

	flag.Parse()

	vsockPortPath := fmt.Sprintf("%s_%d", *vsockPath, *vsockPort)
	lis, err := net.Listen("unix", vsockPortPath)
	if err != nil {
		panic(err)
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt)
	go func() {
		<-done

		log.Println("Exiting gracefully")

		_ = lis.Close()
		_ = os.Remove(vsockPortPath)
	}()

	log.Println("Listening on", lis.Addr())

	clients := 0
	for {
		conn, err := lis.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}

			log.Println("could not accept connection, continuing:", err)

			return
		}

		clients++

		log.Printf("%v clients connected", clients)

		go func() {
			defer func() {
				_ = conn.Close()

				clients--

				log.Printf("%v clients connected", clients)

				if err := recover(); err != nil {
					log.Printf("Client disconnected with error: %v", err)
				}
			}()

			lines := bufio.NewScanner(conn)

			for lines.Scan() {
				fmt.Println(lines.Text())
			}
		}()
	}
}
