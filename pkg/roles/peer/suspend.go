package peer

import (
	"context"
	"time"
)

func (resumedPeer *ResumedPeer) SuspendAndCloseAgentServer(ctx context.Context, resumeTimeout time.Duration) error {
	return resumedPeer.resumedRunner.SuspendAndCloseAgentServer(
		ctx,

		resumeTimeout,
	)
}
