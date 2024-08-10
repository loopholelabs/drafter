package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/drafter/pkg/utils"
)

func main() {
	vsockPort := flag.Int("vsock-port", 25, "VSock port")
	vsockTimeout := flag.Duration("vsock-timeout", time.Minute, "VSock dial timeout")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var errs error
	defer func() {
		if errs != nil {
			panic(errs)
		}
	}()

	panicHandler := utils.NewPanicHandler(
		ctx,
		&errs,
		utils.GetPanicHandlerHooks{},
	)
	defer panicHandler.Wait()
	defer panicHandler.Cancel()
	defer panicHandler.HandlePanics(false)()

	go func() {
		done := make(chan os.Signal, 1)
		signal.Notify(done, os.Interrupt)

		<-done

		log.Println("Exiting gracefully")

		cancel()
	}()

	log.Println("Sending liveness ping")

	dialCtx, cancelDialCtx := context.WithTimeout(panicHandler.InternalCtx, *vsockTimeout)
	defer cancelDialCtx()

	if err := ipc.SendLivenessPing(
		dialCtx,

		ipc.VSockCIDHost,
		uint32(*vsockPort),
	); err != nil {
		panic(err)
	}

	log.Println("Sent liveness ping")

	log.Println("Shutting down")
}
