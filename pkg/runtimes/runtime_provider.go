package runtimes

import (
	"context"
	"time"

	"github.com/loopholelabs/silo/pkg/storage/devicegroup"
)

/**
 * Interface to a runtime
 *
 */
type RuntimeProviderIfc interface {
	Start(ctx context.Context, rescueCtx context.Context, errChan chan error) error
	Resume(ctx context.Context, rescueTimeout time.Duration, dg *devicegroup.DeviceGroup, errChan chan error) error
	Suspend(ctx context.Context, timeout time.Duration, dg *devicegroup.DeviceGroup) error
	FlushData(ctx context.Context, dg *devicegroup.DeviceGroup) error
	FlushDevices(ctx context.Context, dg *devicegroup.DeviceGroup) error
	Close(dg *devicegroup.DeviceGroup) error

	DevicePath() string
	GetVMPid() int
}
