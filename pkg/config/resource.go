package config

type ResourceAddresses struct {
	State     string
	Memory    string
	Initramfs string
	Kernel    string
	Disk      string
	Config    string
}

type ResourceResumeThresholds struct {
	State     int64
	Memory    int64
	Initramfs int64
	Kernel    int64
	Disk      int64
	Config    int64
}
