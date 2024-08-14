package runner

import "errors"

var (
	ErrCouldNotWaitForFirecracker     = errors.New("could not wait for firecracker")
	ErrCouldNotCloseServer            = errors.New("could not close server")
	ErrCouldNotRemoveVMDir            = errors.New("could not remove VM directory")
	ErrCouldNotCloseAgent             = errors.New("could not close agent")
	ErrCouldNotChownVSockPath         = errors.New("could not change ownership of vsock path")
	ErrCouldNotResumeSnapshot         = errors.New("could not resume snapshot")
	ErrCouldNotAcceptAgent            = errors.New("could not accept agent")
	ErrCouldNotCallAfterResumeRPC     = errors.New("could not call AfterResume RPC")
	ErrCouldNotCallBeforeSuspendRPC   = errors.New("could not call BeforeSuspend RPC")
	ErrCouldNotCreateRecoverySnapshot = errors.New("could not create recovery snapshot")
)
