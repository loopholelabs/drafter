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
	conn, err := vsock.DialContext(ctx, vsockCID, vsockPort)
	if err != nil {
		return errors.Join(ErrCouldNotDialLivenessVSockConnection, err)
	}

	if err := conn.Close(); err != nil {
		return errors.Join(ErrCouldNotCloseLivenessVSockConnection, err)
	}

	return nil
}
