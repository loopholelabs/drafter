package ipc

import (
	"context"

	"github.com/loopholelabs/drafter/internal/vsock"
)

func SendLivenessPing(
	ctx context.Context,

	vsockCID uint32,
	vsockPort uint32,
) error {
	conn, err := vsock.DialContext(ctx, vsockCID, vsockPort)
	if err != nil {
		return err
	}

	return conn.Close()
}
