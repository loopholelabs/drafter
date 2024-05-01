package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"

	"github.com/loopholelabs/drafter/pkg/utils"
	"github.com/loopholelabs/drafter/pkg/vsock"
)

func main() {
	vsockPort := flag.Int("vsock-port", 25, "VSock port")

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

	if err := vsock.SendLivenessPing(
		ctx,

		vsock.CIDHost,
		uint32(*vsockPort),
	); err != nil {
		panic(err)
	}

	log.Println("Shutting down")
}
