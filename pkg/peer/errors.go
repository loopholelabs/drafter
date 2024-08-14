package peer

import "errors"

var (
	ErrConfigFileNotFound                 = errors.New("config file not found")
	ErrCouldNotGetNBDDeviceStat           = errors.New("could not get NBD device stat")
	ErrCouldNotStartRunner                = errors.New("could not start runner")
	ErrPeerContextCancelled               = errors.New("peer context cancelled")
	ErrCouldNotCreateDeviceNode           = errors.New("could not create device node")
	ErrCouldNotCloseMigratedPeer          = errors.New("could not close migrated peer")
	ErrCouldNotOpenConfigFile             = errors.New("could not open config file")
	ErrCouldNotDecodeConfigFile           = errors.New("could not decode config file")
	ErrCouldNotResumeRunner               = errors.New("could not resume runner")
	ErrCouldNotCreateMigratablePeer       = errors.New("could not create migratable peer")
	ErrCouldNotSuspendAndCloseAgentServer = errors.New("could not suspend and close agent server")
	ErrCouldNotMsyncRunner                = errors.New("could not msync runner")
)
