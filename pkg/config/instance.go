package config

type NetworkConfiguration struct {
	Interface string
	MAC       string
}

type VMConfiguration struct {
	CPUCount    int
	MemorySize  int
	CPUTemplate string

	BootArgs string
}
