package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"time"

	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/logging"
)

func main() {
	vsockPort := flag.Int("vsock-port", 25, "VSock port")
	vsockTimeout := flag.Duration("vsock-timeout", time.Minute, "VSock dial timeout")

	flag.Parse()

	log := logging.New(logging.Zerolog, "agent", os.Stderr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt)
	go func() {
		<-done
		log.Info().Msg("Exiting gracefully")
		cancel()
	}()

	log.Info().Msg("Sending liveness ping")

	dialCtx, cancelDialCtx := context.WithTimeout(ctx, *vsockTimeout)
	defer cancelDialCtx()

	err := ipc.SendLivenessPing(dialCtx, ipc.VSockCIDHost, uint32(*vsockPort))
	if err != nil {
		log.Error().Err(err).Msg("Error sending liveness ping")
	} else {
		log.Info().Msg("Sent liveness ping")
	}

	log.Info().Msg("Shutting down")
}
