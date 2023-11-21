package config

const (
	InitramfsName = "architekt.arkinitramfs"
	KernelName    = "architekt.arkkernel"
	DiskName      = "architekt.arkdisk"

	StateName  = "architekt.arkstate"
	MemoryName = "architekt.arkmemory"

	PackageConfigName = "architekt.arkconfig"
)

type PackageConfiguration struct {
	InitramfsName string
	KernelName    string
	DiskName      string

	StateName  string
	MemoryName string

	PackageConfigName string
}
