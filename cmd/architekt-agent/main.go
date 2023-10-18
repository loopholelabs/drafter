package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/loopholelabs/architekt/pkg/vsock"
)

func main() {
	vsockPort := flag.Int("vsock-port", 26, "VSock port")

	shellCmd := flag.String("shell-cmd", "sh", "Shell to use to run the before suspend and after resume commands")
	beforeSuspendCmd := flag.String("before-suspend-cmd", "", "Command to run before the VM is suspended (leave empty to disable)")
	afterResumeCmd := flag.String("after-resume-cmd", "", "Command to run after the VM has been resumed (leave empty to disable)")

	verbose := flag.Bool("verbose", false, "Whether to enable verbose logging")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agent := vsock.NewAgent(
		vsock.CIDGuest,
		uint32(*vsockPort),

		func(ctx context.Context) error {
			log.Println("Suspending app")

			if strings.TrimSpace(*beforeSuspendCmd) != "" {
				cmd := exec.Command(*shellCmd, "-c", *beforeSuspendCmd)
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr

				if err := cmd.Run(); err != nil {
					return err
				}
			}

			return nil
		},
		func(ctx context.Context) error {
			log.Println("Resumed app")

			if strings.TrimSpace(*afterResumeCmd) != "" {
				cmd := exec.Command(*shellCmd, "-c", *afterResumeCmd)
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr

				if err := cmd.Run(); err != nil {
					return err
				}
			}

			return nil
		},

		time.Second*10,

		*verbose,
	)

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()

		if err := agent.Wait(); err != nil {
			panic(err)
		}
	}()

	if err := agent.Open(ctx); err != nil {
		panic(err)
	}
	defer agent.Close()

	log.Println("Listening on", *vsockPort)

	wg.Wait()
}
