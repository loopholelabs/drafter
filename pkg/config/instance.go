package config

type NetworkConfiguration struct {
	Interface string
	MAC       string
}

type VMConfiguration struct {
	CpuCount           int
	MemorySize         int
	PackagePaddingSize int
	BootArgs           string
}
