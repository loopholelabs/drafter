package config

const (
	InitramfsName = "drafter.drftinitramfs"
	KernelName    = "drafter.drftkernel"
	DiskName      = "drafter.drftdisk"

	StateName  = "drafter.drftstate"
	MemoryName = "drafter.drftmemory"

	PackageConfigName = "drafter.drftconfig"
)

type PackageConfiguration struct {
	InitramfsName string
	KernelName    string
	DiskName      string

	StateName  string
	MemoryName string

	PackageConfigName string
}
