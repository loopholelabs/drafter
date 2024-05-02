package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/loopholelabs/drafter/pkg/utils"
	"github.com/loopholelabs/drafter/pkg/vsock"
)

func main() {
	vsockPort := flag.Int("vsock-port", 25, "VSock port")

	connectTimeout := flag.Duration("connect-timeout", time.Minute, "Maximum amount of time to wait for liveness to connect")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var errs error
	defer func() {
		if errs != nil {
			panic(errs)
		}
	}()

	ctx, handlePanics, _, cancel, wait, _ := utils.GetPanicHandler(
		ctx,
		&errs,
		utils.GetPanicHandlerHooks{},
	)
	defer wait()
	defer cancel()
	defer handlePanics(false)()

	go func() {
		done := make(chan os.Signal, 1)
		signal.Notify(done, os.Interrupt)

		<-done

		log.Println("Exiting gracefully")

		cancel()
	}()

	log.Println("Sending liveness ping")

	dialCtx, cancelDialCtx := context.WithTimeout(ctx, *connectTimeout)
	defer cancelDialCtx()

	if err := vsock.SendLivenessPing(
		dialCtx,

		vsock.CIDHost,
		uint32(*vsockPort),
	); err != nil {
		panic(err)
	}

	log.Println("Sent liveness ping")

	log.Println("Shutting down")
}
