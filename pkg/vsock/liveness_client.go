package vsock

import (
	"context"
)

func SendLivenessPing(
	ctx context.Context,

	vsockCID uint32,
	vsockPort uint32,
) error {
	conn, err := DialContext(ctx, vsockCID, vsockPort)
	if err != nil {
		return err
	}

	return conn.Close()
}
