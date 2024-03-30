package vsock

import (
	ivsock "github.com/mdlayher/vsock"
)

func Listen(cid uint32, port uint32, backlog int) (*ivsock.Listener, error) {
	return ivsock.ListenContextID(cid, port, nil)
}
