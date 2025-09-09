package main

import (
	"context"
	"flag"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime/debug"
	"strings"
	"time"

	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/drafter/pkg/runtimes/firecracker/vsock"
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

	// Get vcs revision
	var Commit = func() string {
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, setting := range info.Settings {
				if setting.Key == "vcs.revision" {
					return setting.Value
				}
			}
		}
		return ""
	}()

	log.Info().Str("vcs.revision", Commit).Msg("Starting agent")

	log.Info().Uint("vsock-port", *vsockPort).
		Int64("vsock-timeout-ms", (*vsockTimeout).Milliseconds()).
		Str("shell-cmd", *shellCmd).
		Str("before-suspend-cmd", *beforeSuspendCmd).
		Str("after-resume-cmd", *afterResumeCmd).
		Msg("Starting drafter-agent NEW")

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

	err := connectAndHandle(log, ctx, *vsockTimeout, int(*vsockPort), agentClient)

	if err != nil {
		log.Error().Err(err).Msg("Error from RPC connection")
	}

}

/**
 * Connect to the host and handle any RPC calls.
 * Waits until ctx is cancelled.
 */
func connectAndHandle(log loggingtypes.Logger, ctx context.Context, timeout time.Duration, port int, agentClient *ipc.AgentClientLocal[struct{}]) error {
	log.Info().Msg("Connecting to host")

	connFactory := func(ctx context.Context) (io.ReadWriteCloser, error) {
		dialCtx, cancelDialCtx := context.WithTimeout(ctx, timeout)
		defer cancelDialCtx()
		return vsock.DialContext(dialCtx, ipc.VSockCIDHost, uint32(port))
	}

	connectedAgentClient, err := ipc.StartAgentClient[*ipc.AgentClientLocal[struct{}], struct{}](
		log, connFactory, agentClient,
	)

	if err != nil {
		return err
	}
	defer func() {
		err := connectedAgentClient.Close()
		if err != nil {
			log.Info().Err(err).Msg("Error closing agent")
		}
	}()

	log.Info().Msg("Connected to host, serving RPC calls")

	// Wait until ctx is cancelled
	<-ctx.Done()
	return nil
}
