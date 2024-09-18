package ipc

import (
	"context"
	"errors"

	"github.com/loopholelabs/drafter/internal/vsock"
)

var (
	ErrCouldNotDialLivenessVSockConnection  = errors.New("could not dial VSock liveness connection")
	ErrCouldNotCloseLivenessVSockConnection = errors.New("could not close VSock liveness connection")
)

func SendLivenessPing(
	ctx context.Context,
	vsockCID uint32,
	vsockPort uint32,
) error {
	if _, err := vsock.DialContext(ctx, vsockCID, vsockPort); err != nil {
		return errors.Join(ErrCouldNotDialLivenessVSockConnection, err)
	}

	// We don't `conn.Close` here since Firecracker handles resetting active VSock connections for us

	return nil
}
