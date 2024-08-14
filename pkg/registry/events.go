package registry

type CustomEventType byte

const (
	EventCustomAllDevicesSent    = CustomEventType(0)
	EventCustomTransferAuthority = CustomEventType(1)
)
