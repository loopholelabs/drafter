package v1

type BootSource struct {
	InitrdPath      string `json:"initrd_path"`
	KernelImagePath string `json:"kernel_image_path"`
	BootArgs        string `json:"boot_args"`
}

type Drive struct {
	DriveID      string `json:"drive_id"`
	PathOnHost   string `json:"path_on_host"f`
	IsRootDevice bool   `json:"is_root_device"`
	IsReadOnly   bool   `json:"is_read_only"`
}

type MachineConfig struct {
	VCPUCount  int `json:"vcpu_count"`
	MemSizeMib int `json:"mem_size_mib"`
}

type Action struct {
	ActionType string `json:"action_type"`
}

type NetworkInterface struct {
	IfaceID     string `json:"iface_id"`
	GuestMAC    string `json:"guest_mac"`
	HostDevName string `json:"host_dev_name"`
}

type VirtualMachineStateRequest struct {
	State string `json:"state"`
}

type SnapshotCreateRequest struct {
	SnapshotType   string `json:"snapshot_type"`
	SnapshotPath   string `json:"snapshot_path"`
	MemoryFilePath string `json:"mem_file_path"`
}

type SnapshotLoadRequest struct {
	SnapshotPath         string                           `json:"snapshot_path"`
	MemoryBackend        SnapshotLoadRequestMemoryBackend `json:"mem_backend"`
	EnableDiffSnapshots  bool                             `json:"enable_diff_snapshots"`
	ResumeVirtualMachine bool                             `json:"resume_vm"`
}

type SnapshotLoadRequestMemoryBackend struct {
	BackendPath string `json:"backend_path"`
	BackendType string `json:"backend_type"`
}

type SnapshotNoMemoryCreateRequest struct {
	SnapshotPath string `json:"snapshot_path"`
}

type VSock struct {
	GuestCID int    `json:"guest_cid"`
	UDSPath  string `json:"uds_path"`
}
