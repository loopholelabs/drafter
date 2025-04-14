package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"time"

	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/logging"
	loggingtypes "github.com/loopholelabs/logging/types"
)

func main() {
	vsockPort := flag.Uint("vsock-port", 26, "VSock port")
	vsockTimeout := flag.Duration("vsock-timeout", time.Minute, "VSock dial timeout")
	shellCmd := flag.String("shell-cmd", "sh", "Shell to use to run the before suspend and after resume commands")
	beforeSuspendCmd := flag.String("before-suspend-cmd", "", "Command to run before the VM is suspended (leave empty to disable)")
	afterResumeCmd := flag.String("after-resume-cmd", "", "Command to run after the VM has been resumed (leave empty to disable)")

	flag.Parse()

	log := logging.New(logging.Zerolog, "agent", os.Stderr)

	log.Info().Uint("vsock-port", *vsockPort).
		Int64("vsock-timeout-ms", (*vsockTimeout).Milliseconds()).
		Str("shell-cmd", *shellCmd).
		Str("before-suspend-cmd", *beforeSuspendCmd).
		Str("after-resume-cmd", *afterResumeCmd).
		Msg("Starting drafter-agent")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt)

	go func() {
		<-done
		log.Info().Msg("Exiting gracefully")
		cancel()
	}()

	beforeSuspendFn := func(ctx context.Context) error {
		log.Info().Str("shell-cmd", *shellCmd).
			Str("before-suspend-cmd", *beforeSuspendCmd).
			Msg("Running pre-suspend command")
		if strings.TrimSpace(*beforeSuspendCmd) != "" {
			cmd := exec.CommandContext(ctx, *shellCmd, "-c", *beforeSuspendCmd)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			err := cmd.Run()
			if err != nil {
				return err
			}
		}
		return nil
	}

	afterResumeFn := func(ctx context.Context) error {
		log.Info().Str("shell-cmd", *shellCmd).
			Str("after-resume-cmd", *afterResumeCmd).
			Msg("Running after-resume command")
		if strings.TrimSpace(*afterResumeCmd) != "" {
			cmd := exec.CommandContext(ctx, *shellCmd, "-c", *afterResumeCmd)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			err := cmd.Run()
			if err != nil {
				return err
			}
		}
		return nil
	}

	agentClient := ipc.NewAgentClient[struct{}](struct{}{}, beforeSuspendFn, afterResumeFn)

	for {
		err := connectAndHandle(log, ctx, *vsockTimeout, int(*vsockPort), agentClient)

		// Context cancelled, lets quit.
		if errors.Is(err, context.Canceled) {
			break
		}

		if err != nil {
			log.Error().Err(err).Msg("Disconnected from host with error, reconnecting:")
			continue
		}
		break
	}

	log.Info().Msg("Shutting down")
}

/**
 * Connect to the host and handle any RPC calls.
 *
 */
func connectAndHandle(log loggingtypes.Logger, ctx context.Context, timeout time.Duration, port int, agentClient *ipc.AgentClientLocal[struct{}]) error {
	log.Info().Msg("Connecting to host")

	dialCtx, cancelDialCtx := context.WithTimeout(ctx, timeout)
	defer cancelDialCtx()

	connectedAgentClient, err := ipc.StartAgentClient[*ipc.AgentClientLocal[struct{}], struct{}](
		dialCtx,
		ctx,
		ipc.VSockCIDHost,
		uint32(port),
		agentClient,
		ipc.StartAgentClientHooks[struct{}]{},
	)

	if err != nil {
		return err
	}
	defer connectedAgentClient.Close()

	log.Info().Msg("Connected to host, serving RPC calls")
	return connectedAgentClient.Wait()
}
