package registry

import "errors"

var (
	ErrCouldNotGetInputDeviceStatistics   = errors.New("could not get input device statistics")
	ErrCouldNotCreateNewDevice            = errors.New("could not create new device")
	ErrCouldNotSendDeviceInfo             = errors.New("could not send device info")
	ErrCouldNotSendEvent                  = errors.New("could not send event")
	ErrCouldNotMigrate                    = errors.New("could not migrate")
	ErrCouldNotContinueWithMigration      = errors.New("could not continue with migration")
	ErrCouldNotHandleProtocol             = errors.New("could not handle protocol")
	ErrCouldNotHandleNeedAt               = errors.New("could not handle NeedAt")
	ErrCouldNotHandleDontNeedAt           = errors.New("could not handle DontNeedAt")
	ErrCouldNotCreateMigrator             = errors.New("could not create migrator")
	ErrCouldNotWaitForMigrationCompletion = errors.New("could not wait for migration completion")
)
