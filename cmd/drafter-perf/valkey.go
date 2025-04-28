package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"runtime/pprof"
	"time"

	"github.com/google/uuid"
	"github.com/valkey-io/valkey-go"
)

// Run some benchmark against the valkey vm
func benchValkey(name string, port int, iterations int) (time.Duration, time.Duration, error) {
	var err error
	if profileCPU {
		f, err := os.Create(fmt.Sprintf("%s.prof", name))
		if err != nil {
			panic(err)
		}
		pprof.StartCPUProfile(f)
		defer func() {
			pprof.StopCPUProfile()
			f.Close()
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
