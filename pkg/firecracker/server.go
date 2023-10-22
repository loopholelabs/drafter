package firecracker

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
	"k8s.io/utils/inotify"
)

var (
	ErrNoSocketCreated = errors.New("no socket created")

	errSignalKilled = errors.New("signal: killed")
)

const (
	FirecrackerSocketName = "firecracker.sock"
)

type Server struct {
	firecrackerBin string
	jailerBin      string

	chrootBaseDir string

	uid int
	gid int

	netns         string
	numaNode      int
	cgroupVersion int

	enableOutput bool
	enableInput  bool

	cmd   *exec.Cmd
	vmDir string

	wg sync.WaitGroup

	closeLock sync.Mutex

	errs chan error
}

func NewServer(
	firecrackerBin string,
	jailerBin string,

	chrootBaseDir string,

	uid int,
	gid int,

	netns string,
	numaNode int,
	cgroupVersion int,

	enableOutput bool,
	enableInput bool,
) *Server {
	return &Server{
		firecrackerBin: firecrackerBin,
		jailerBin:      jailerBin,

		chrootBaseDir: chrootBaseDir,

		uid: uid,
		gid: gid,

		netns:         netns,
		numaNode:      numaNode,
		cgroupVersion: cgroupVersion,

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

func (s *Server) Open() (string, error) {
	id := uuid.NewString()

	s.vmDir = filepath.Join(s.chrootBaseDir, "firecracker", id, "root")
	if err := os.MkdirAll(s.vmDir, os.ModePerm); err != nil {
		return "", err
	}

	watcher, err := inotify.NewWatcher()
	if err != nil {
		return "", err
	}
	defer watcher.Close()

	if err := watcher.AddWatch(s.vmDir, inotify.InCreate); err != nil {
		return "", err
	}

	cpus, err := os.ReadFile(filepath.Join("/sys", "devices", "system", "node", fmt.Sprintf("node%v", s.numaNode), "cpulist"))
	if err != nil {
		return "", err
	}

	s.cmd = exec.Command(
		s.jailerBin,
		"--chroot-base-dir",
		s.chrootBaseDir,
		"--uid",
		fmt.Sprintf("%v", s.uid),
		"--gid",
		fmt.Sprintf("%v", s.gid),
		"--netns",
		filepath.Join("/var", "run", "netns", s.netns),
		"--cgroup-version",
		fmt.Sprintf("%v", s.cgroupVersion),
		"--cgroup",
		fmt.Sprintf("cpuset.mems=%v", s.numaNode),
		"--cgroup",
		fmt.Sprintf("cpuset.cpus=%s", cpus),
		"--id",
		id,
		"--exec-file",
		s.firecrackerBin,
		"--",
		"--api-sock",
		FirecrackerSocketName,
	)

	if s.enableOutput {
		s.cmd.Stdout = os.Stdout
		s.cmd.Stderr = os.Stderr
	}

	if s.enableInput {
		s.cmd.Stdin = os.Stdin
	} else {
		// Don't forward CTRL-C etc. signals from parent to child process
		// We can't enable this if we set the cmd stdin or we deadlock
		s.cmd.SysProcAttr = &unix.SysProcAttr{
			Setpgid: true,
			Pgid:    0,
		}
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

		if s.errs != nil {
			close(s.errs)

			s.errs = nil
		}
	}()

	socketPath := filepath.Join(s.vmDir, FirecrackerSocketName)
	for ev := range watcher.Event {
		if filepath.Clean(ev.Name) == filepath.Clean(socketPath) {
			return s.vmDir, nil
		}
	}

	return "", ErrNoSocketCreated
}

func (s *Server) Close() error {
	s.closeLock.Lock()
	defer s.closeLock.Unlock()

	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}

	s.wg.Wait()

	if s.errs != nil {
		close(s.errs)

		s.errs = nil
	}

	return nil
}
