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

type Server struct {
	bin string

	verbose      bool
	enableOutput bool
	enableInput  bool

	socketDir string
	cmd       *exec.Cmd

	wg   sync.WaitGroup
	errs chan error
}

func NewServer(
	bin string,

	verbose bool,
	enableOutput bool,
	enableInput bool,
) *Server {
	return &Server{
		bin: bin,

		verbose:      verbose,
		enableOutput: enableOutput,
		enableInput:  enableInput,

		wg:   sync.WaitGroup{},
		errs: make(chan error),
	}
}

func (s *Server) Wait() error {
	for err := range s.errs {
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *Server) Start() (string, error) {
	var err error
	s.socketDir, err = os.MkdirTemp("", "")
	if err != nil {
		return "", err
	}

	watcher, err := inotify.NewWatcher()
	if err != nil {
		return "", err
	}
	defer watcher.Close()

	if err := watcher.AddWatch(s.socketDir, inotify.InCreate); err != nil {
		return "", err
	}

	socketPath := filepath.Join(s.socketDir, "firecracker.sock")

	execLine := []string{s.bin, "--api-sock", socketPath}
	if s.verbose {
		execLine = append(execLine, "--level", "Debug", "--log-path", "/dev/stderr")
	}

	s.cmd = exec.Command(execLine[0], execLine[1:]...)
	if s.enableOutput {
		s.cmd.Stdout = os.Stdout
		s.cmd.Stderr = os.Stderr
	}

	if s.enableInput {
		s.cmd.Stdin = os.Stdin
	}

	if err := s.cmd.Start(); err != nil {
		return "", err
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		if err := s.cmd.Wait(); err != nil && err.Error() != errSignalKilled.Error() {
			s.errs <- err

			return
		}

		close(s.errs)
	}()

	for ev := range watcher.Event {
		if ev.Name == socketPath {
			return socketPath, nil
		}
	}

	return "", ErrNoSocketCreated
}

func (s *Server) Stop() error {
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}

	s.wg.Wait()

	_ = os.RemoveAll(s.socketDir)

	return nil
}
