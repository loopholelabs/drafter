package peer

import (
	"context"
	"time"
)

func (resumedPeer *ResumedPeer[L, R]) SuspendAndCloseAgentServer(ctx context.Context, resumeTimeout time.Duration) error {
	return resumedPeer.resumedRunner.SuspendAndCloseAgentServer(
		ctx,

		resumeTimeout,
	)
}
