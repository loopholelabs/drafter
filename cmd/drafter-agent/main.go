package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"time"

	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/drafter/pkg/utils"
)

func main() {
	vsockPort := flag.Uint("vsock-port", 26, "VSock port")
	vsockTimeout := flag.Duration("vsock-timeout", time.Minute, "VSock dial timeout")

	shellCmd := flag.String("shell-cmd", "sh", "Shell to use to run the before suspend and after resume commands")
	beforeSuspendCmd := flag.String("before-suspend-cmd", "", "Command to run before the VM is suspended (leave empty to disable)")
	afterResumeCmd := flag.String("after-resume-cmd", "", "Command to run after the VM has been resumed (leave empty to disable)")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var errs error
	defer func() {
		if errs != nil {
			panic(errs)
		}
	}()

	goroutineManager := utils.NewGoroutineManager(
		ctx,
		&errs,
		utils.GoroutineManagerHooks{},
	)
	defer goroutineManager.WaitForForegroundGoroutines()
	defer goroutineManager.StopAllGoroutines()
	defer goroutineManager.CreateBackgroundPanicCollector()()

	go func() {
		done := make(chan os.Signal, 1)
		signal.Notify(done, os.Interrupt)

		<-done

		log.Println("Exiting gracefully")

		cancel()
	}()

	agentClient := ipc.NewAgentClient(
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
	)

	for {
		if err := func() error {
			log.Println("Connecting to host")

			dialCtx, cancelDialCtx := context.WithTimeout(goroutineManager.GetGoroutineCtx(), *vsockTimeout)
			defer cancelDialCtx()

			connectedAgentClient, err := ipc.StartAgentClient(
				dialCtx,
				goroutineManager.GetGoroutineCtx(),

				ipc.VSockCIDHost,
				uint32(*vsockPort),

				agentClient,
			)
			if err != nil {
				return err
			}
			defer connectedAgentClient.Close()

			log.Println("Connected to host")

			return connectedAgentClient.Wait()
		}(); err != nil {
			if !(errors.Is(err, context.Canceled) && errors.Is(context.Cause(goroutineManager.GetGoroutineCtx()), goroutineManager.GetErrGoroutineStopped())) {
				log.Println("Disconnected from host with error, reconnecting:", err)

				continue
			}

			panic(err)
		}

		break
	}

	log.Println("Shutting down")
}
