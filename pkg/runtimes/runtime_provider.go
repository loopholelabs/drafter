package runtimes

import (
	"context"
	"time"
)

/**
 * Interface to a runtime
 *
 */
type RuntimeProviderIfc interface {
	Start(ctx context.Context, rescueCtx context.Context) error
	Resume(resumeTimeout time.Duration, rescueTimeout time.Duration) error
	Suspend(ctx context.Context, timeout time.Duration) error
	FlushData(ctx context.Context) error
	Close() error

	DevicePath() string
	GetVMPid() int
}
