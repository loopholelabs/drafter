package terminator

import "errors"

var (
	ErrCouldNotCreateReceivingDirectory = errors.New("could not create receiving directory")
	ErrCouldNotSendNeedAt               = errors.New("could not send NeedAt")
	ErrCouldNotSendDontNeedAt           = errors.New("could not send DontNeedAt")
	ErrCouldNotCloseDevice              = errors.New("could not close device")
	ErrCouldNotCreateDevice             = errors.New("could not create device")
	ErrCouldNotHandleReadAt             = errors.New("could not handle ReadAt")
	ErrCouldNotHandleWriteAt            = errors.New("could not handle WriteAt")
	ErrCouldNotHandleDevInfo            = errors.New("could not handle device info")
	ErrCouldNotHandleEvent              = errors.New("could not handle event")
	ErrCouldNotHandleDirtyList          = errors.New("could not handle dirty list")
	ErrUnknownDeviceName                = errors.New("unknown device name")
)
