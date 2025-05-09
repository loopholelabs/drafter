package runtimes

import (
	"context"
	"time"

	"github.com/loopholelabs/silo/pkg/storage/devicegroup"
)

type EmptyRuntimeProvider struct {
	HomePath string
}

func (rp *EmptyRuntimeProvider) Start(ctx context.Context, rescueCtx context.Context, errChan chan error) error {
	return nil
}

func (rp *EmptyRuntimeProvider) Close(dg *devicegroup.DeviceGroup) error {
	return nil
}

func (rp *EmptyRuntimeProvider) DevicePath() string {
	return rp.HomePath
}

func (rp *EmptyRuntimeProvider) GetVMPid() int {
	return 0
}

func (rp *EmptyRuntimeProvider) Suspend(ctx context.Context, timeout time.Duration, dg *devicegroup.DeviceGroup) error {
	return nil
}

func (rp *EmptyRuntimeProvider) FlushData(ctx context.Context, dg *devicegroup.DeviceGroup) error {
	return nil
}

func (rp *EmptyRuntimeProvider) Resume(resumeTimeout time.Duration, rescueTimeout time.Duration, errChan chan error) error {
	return nil
}
