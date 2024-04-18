package config

type NetworkConfiguration struct {
	Interface string
	MAC       string
}

type VMConfiguration struct {
	CpuCount    int
	MemorySize  int
	CPUTemplate string

	BootArgs string
}
