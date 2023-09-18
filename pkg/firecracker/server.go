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
	bin        string
	socketPath string
	pwd        string

	verbose      bool
	enableOutput bool
	enableInput  bool

	cmd *exec.Cmd

	wg   sync.WaitGroup
	errs chan error
}

func NewServer(
	bin string,
	socketPath string,
	pwd string,

	verbose bool,
	enableOutput bool,
	enableInput bool,
) *Server {
	return &Server{
		bin:        bin,
		socketPath: socketPath,
		pwd:        pwd,

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

func (s *Server) Open() error {
	watcher, err := inotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	if err := watcher.AddWatch(filepath.Dir(s.socketPath), inotify.InCreate); err != nil {
		return err
	}

	execLine := []string{s.bin, "--api-sock", s.socketPath}
	if s.verbose {
		execLine = append(execLine, "--level", "Debug", "--log-path", "/dev/stderr")
	}

	s.cmd = exec.Command(execLine[0], execLine[1:]...)
	s.cmd.Dir = s.pwd
	if s.enableOutput {
		s.cmd.Stdout = os.Stdout
		s.cmd.Stderr = os.Stderr
	}

	if s.enableInput {
		s.cmd.Stdin = os.Stdin
	}

	if err := s.cmd.Start(); err != nil {
		return err
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
		if filepath.Clean(ev.Name) == filepath.Clean(s.socketPath) {
			return nil
		}
	}

	return ErrNoSocketCreated
}

func (s *Server) Close() error {
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}

	s.wg.Wait()

	_ = os.Remove(s.socketPath)

	return nil
}
