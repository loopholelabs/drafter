package peer

import "errors"

var (
	ErrConfigFileNotFound           = errors.New("config file not found")
	ErrCouldNotStartRunner          = errors.New("could not start runner")
	ErrPeerContextCancelled         = errors.New("peer context cancelled")
	ErrCouldNotCloseMigratedPeer    = errors.New("could not close migrated peer")
	ErrCouldNotOpenConfigFile       = errors.New("could not open config file")
	ErrCouldNotDecodeConfigFile     = errors.New("could not decode config file")
	ErrCouldNotResumeRunner         = errors.New("could not resume runner")
	ErrCouldNotCreateMigratablePeer = errors.New("could not create migratable peer")
)
