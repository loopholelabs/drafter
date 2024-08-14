package packager

const (
	KernelName = "kernel"
	DiskName   = "disk"

	StateName  = "state"
	MemoryName = "memory"

	ConfigName = "config"
)

var (
	KnownNames = []string{
		KernelName,
		DiskName,

		StateName,
		MemoryName,

		ConfigName,
	}
)
