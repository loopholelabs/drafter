package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"os"
	"runtime/pprof"
	"time"

	"github.com/google/uuid"
	"github.com/valkey-io/valkey-go"
)

// Run some benchmark against the valkey vm
func benchValkey(profileCPU bool, name string, port int, iterations int) (time.Duration, time.Duration, error) {
	var err error
	if profileCPU {
		f, err := os.Create(fmt.Sprintf("%s.prof", name))
		if err != nil {
			panic(err)
		}
		err = pprof.StartCPUProfile(f)
		if err != nil {
			panic(err)
		}

		defer func() {
			pprof.StopCPUProfile()
			_ = f.Close()
		}()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var client valkey.Client

	// Retry connection loop
	for i := 0; i < 30; i++ {
		client, err = valkey.NewClient(valkey.ClientOption{InitAddress: []string{fmt.Sprintf("127.0.0.1:%d", port)}})
		if err == nil {
			break
		}
		time.Sleep(10 * time.Second)
	}

	if client == nil {
		return 0, 0, errors.New("could not connect")
	}

	// SET key val NX
	ctime := time.Now()
	for i := 0; i < iterations; i++ {
		key := fmt.Sprintf("key-%d", i)
		val := uuid.NewString()
		err = client.Do(ctx, client.B().Set().Key(key).Value(val).Nx().Build()).Error()
		if err != nil {
			return 0, 0, err
		}
	}
	timeSet := time.Since(ctime)

	ctime = time.Now()
	for _, k := range rand.Perm(iterations) {
		key := fmt.Sprintf("key-%d", k)
		_, err = client.Do(ctx, client.B().Get().Key(key).Build()).ToString()
		if err != nil {
			return 0, 0, err
		}
	}
	timeGet := time.Since(ctime)

	client.Close()

	return timeSet, timeGet, nil
}

type ValkeyWaitReady struct {
	Up      bool
	Timeout time.Duration
}

func (vwr *ValkeyWaitReady) Ready() error {
	// Try to connect to valkey
	ctx, cancel := context.WithTimeout(context.Background(), vwr.Timeout)
	defer cancel()
	ticker := time.NewTicker(1 * time.Second)
	for {
		select {
		case <-ticker.C:
			// Try to connect to valkey
			con, err := net.Dial("tcp", "127.0.0.1:3333")
			if err == nil {
				_ = con.Close()
				fmt.Printf(" ### Valkey up!\n")
				vwr.Up = true
				return nil
			}
		case <-ctx.Done():
			fmt.Printf(" ### Unable to connect to valkey!\n")
			return ctx.Err()
		}
	}
}
