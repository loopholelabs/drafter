package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime/pprof"
	"time"
)

func benchCICD(profileCPU bool, name string, timeout time.Duration) error {
	err := portCallback(4568, timeout)
	if err != nil {
		return err
	}

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

	return portCallback(4567, timeout)
}

func portCallback(port int, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	for {
		select {
		case <-ticker.C:
			// Try to connect to the cicd runner
			con, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
			if err == nil {
				data, err := io.ReadAll(con)
				if err != nil {
					return err
				}
				fmt.Printf("PORT %d said %s\n", port, data)
				con.Close()
				return nil
			}
		case <-ctx.Done():
			return errors.New("Never finished")
		}
	}
}
