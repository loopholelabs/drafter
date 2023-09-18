package vsock

import (
	"fmt"
	"net"
	"os"
)

type LivenessPingReceiver struct {
	vsockPortPath string

	lis net.Listener
}

func NewLivenessPingReceiver(
	vsockPath string,
	vsockPort uint32,
) *LivenessPingReceiver {
	return &LivenessPingReceiver{
		vsockPortPath: fmt.Sprintf("%s_%d", vsockPath, vsockPort),
	}
}

func (l *LivenessPingReceiver) Open() error {
	var err error
	l.lis, err = net.Listen("unix", l.vsockPortPath)
	if err != nil {
		return err
	}

	return nil
}

func (l *LivenessPingReceiver) Receive() error {
	conn, err := l.lis.Accept()
	if err != nil {
		return err
	}
	defer conn.Close()

	return nil
}

func (l *LivenessPingReceiver) Close() {
	_ = os.Remove(l.vsockPortPath)

	if l.lis != nil {
		_ = l.lis.Close()
	}
}
