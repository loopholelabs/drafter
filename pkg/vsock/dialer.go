package vsock

import (
	ivsock "github.com/mdlayher/vsock"
)

func Dial(cid uint32, port uint32) (*ivsock.Conn, error) {
	return ivsock.Dial(cid, port, nil)
}
