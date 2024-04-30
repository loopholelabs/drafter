package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"

	"github.com/loopholelabs/drafter/pkg/vsock"
	"github.com/pojntfx/panrpc/go/pkg/rpc"
)

var (
	errFinished = errors.New("finished")
)

func main() {
	vsockPort := flag.Uint("vsock-port", 26, "VSock port")

	shellCmd := flag.String("shell-cmd", "sh", "Shell to use to run the before suspend and after resume commands")
	beforeSuspendCmd := flag.String("before-suspend-cmd", "", "Command to run before the VM is suspended (leave empty to disable)")
	afterResumeCmd := flag.String("after-resume-cmd", "", "Command to run after the VM has been resumed (leave empty to disable)")

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

		clients := 0

		for {
			if err := vsock.StartAgentClient(
				ctx,

				vsock.CIDHost,
				uint32(*vsockPort),

				func(ctx context.Context) error {
					log.Println("Running pre-suspend command")

					if strings.TrimSpace(*beforeSuspendCmd) != "" {
						cmd := exec.CommandContext(ctx, *shellCmd, "-c", *beforeSuspendCmd)
						cmd.Stdout = os.Stdout
						cmd.Stderr = os.Stderr

						if err := cmd.Run(); err != nil {
							return err
						}
					}

					return nil
				},
				func(ctx context.Context) error {
					log.Println("Running after-resume command")

					if strings.TrimSpace(*afterResumeCmd) != "" {
						cmd := exec.CommandContext(ctx, *shellCmd, "-c", *afterResumeCmd)
						cmd.Stdout = os.Stdout
						cmd.Stderr = os.Stderr

						if err := cmd.Run(); err != nil {
							return err
						}
					}

					return nil
				},

				&rpc.Options{
					OnClientConnect: func(remoteID string) {
						clients++

						log.Printf("%v clients connected", clients)
					},
					OnClientDisconnect: func(remoteID string) {
						clients--

						log.Printf("%v clients connected", clients)
					},
				},
			); err != nil {
				if !(errors.Is(err, context.Canceled) && errors.Is(context.Cause(ctx), errFinished)) {
					log.Println("Disconnected from host with error, reconnecting:", err)

					continue
				}

				panic(err)
			}

			break
		}

		log.Println("Shutting down")
	}
}
