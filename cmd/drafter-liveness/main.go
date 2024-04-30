package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"

	"github.com/loopholelabs/drafter/pkg/vsock"
)

var (
	errFinished = errors.New("finished")
)

func main() {
	vsockPort := flag.Int("vsock-port", 25, "VSock port")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	{
		var errsLock sync.Mutex
		var errs error

		defer func() {
			if errs != nil {
				panic(errs)
			}
		}()

		var wg sync.WaitGroup
		defer wg.Wait()

		ctx, cancel := context.WithCancelCause(ctx)
		defer cancel(errFinished)

		handleGoroutinePanic := func() func() {
			return func() {
				if err := recover(); err != nil {
					errsLock.Lock()
					defer errsLock.Unlock()

					var e error
					if v, ok := err.(error); ok {
						e = v
					} else {
						e = fmt.Errorf("%v", err)
					}

					if !(errors.Is(e, context.Canceled) && errors.Is(context.Cause(ctx), errFinished)) {
						errs = errors.Join(errs, e)
					}

					cancel(errFinished)
				}
			}
		}

		defer handleGoroutinePanic()()

		go func() {
			done := make(chan os.Signal, 1)
			signal.Notify(done, os.Interrupt)

			<-done

			wg.Add(1) // We only register this here since we still want to be able to exit without manually interrupting
			defer wg.Done()

			defer handleGoroutinePanic()()

			log.Println("Exiting gracefully")

			cancel(errFinished)
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
}
