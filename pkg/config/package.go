package config

const (
	InitramfsName = "drafter.drftinitramfs"
	KernelName    = "drafter.drftkernel"
	DiskName      = "drafter.drftdisk"

	StateName  = "drafter.drftstate"
	MemoryName = "drafter.drftmemory"

	ConfigName = "drafter.drftconfig"
)

type KnownNamesConfiguration struct {
	InitramfsName string
	KernelName    string
	DiskName      string

	StateName  string
	MemoryName string

	ConfigName string
}

type PackageConfiguration struct {
	AgentVSockPort uint32 `json:"agentVSockPort"`
}
