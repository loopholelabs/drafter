package firecracker

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"k8s.io/utils/inotify"
)

var (
	ErrNoSocketCreated = errors.New("no socket created")

	errSignalKilled = errors.New("signal: killed")
)

type FirecrackerInstance struct {
	bin string

	verbose      bool
	enableOutput bool
	enableInput  bool

	socketDir string
	cmd       *exec.Cmd

	wg   sync.WaitGroup
	errs chan error
}

func NewFirecrackerInstance(
	bin string,

	verbose bool,
	enableOutput bool,
	enableInput bool,
) *FirecrackerInstance {
	return &FirecrackerInstance{
		bin: bin,

		verbose:      verbose,
		enableOutput: enableOutput,
		enableInput:  enableInput,

		wg:   sync.WaitGroup{},
		errs: make(chan error),
	}
}

func (i *FirecrackerInstance) Wait() error {
	for err := range i.errs {
		if err != nil {
			return err
		}
	}

	return nil
}

func (i *FirecrackerInstance) Start() (string, error) {
	var err error
	i.socketDir, err = os.MkdirTemp("", "")
	if err != nil {
		return "", err
	}

	watcher, err := inotify.NewWatcher()
	if err != nil {
		return "", err
	}
	defer watcher.Close()

	if err := watcher.AddWatch(i.socketDir, inotify.InCreate); err != nil {
		return "", err
	}

	socketPath := filepath.Join(i.socketDir, "firecracker.sock")

	execLine := []string{i.bin, "--api-sock", socketPath}
	if i.verbose {
		execLine = append(execLine, "--level", "Debug", "--log-path", "/dev/stderr")
	}

	i.cmd = exec.Command(execLine[0], execLine[1:]...)
	if i.enableOutput {
		i.cmd.Stdout = os.Stdout
		i.cmd.Stderr = os.Stderr
	}

	if i.enableInput {
		i.cmd.Stdin = os.Stdin
	}

	if err := i.cmd.Start(); err != nil {
		return "", err
	}

	i.wg.Add(1)
	go func() {
		defer i.wg.Done()

		if err := i.cmd.Wait(); err != nil && err.Error() != errSignalKilled.Error() {
			i.errs <- err

			return
		}
	}()

	for ev := range watcher.Event {
		if ev.Name == socketPath {
			return socketPath, nil
		}
	}

	return "", ErrNoSocketCreated
}

func (i *FirecrackerInstance) Stop() error {
	if i.cmd != nil && i.cmd.Process != nil {
		_ = i.cmd.Process.Kill()
	}

	i.wg.Wait()

	_ = os.RemoveAll(i.socketDir)

	close(i.errs)

	return nil
}
