package utils

type HypervisorConfiguration struct {
	FirecrackerBin string
	JailerBin      string

	ChrootBaseDir string

	UID int
	GID int

	NetNS         string
	NumaNode      int
	CgroupVersion int

	EnableOutput bool
	EnableInput  bool
}

type NetworkConfiguration struct {
	Interface string
	MAC       string
}

type AgentConfiguration struct {
	AgentVSockPort uint32
}

type LivenessConfiguration struct {
	LivenessVSockPort uint32
}

type VMConfiguration struct {
	CpuCount           int
	MemorySize         int
	PackagePaddingSize int
	BootArgs           string
}
