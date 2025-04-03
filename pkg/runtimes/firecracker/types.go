package firecracker

import "time"

type PackageConfiguration struct {
	AgentVSockPort uint32 `json:"agentVSockPort"`
}

type AgentConfiguration struct {
	AgentVSockPort uint32
	ResumeTimeout  time.Duration
}

type LivenessConfiguration struct {
	LivenessVSockPort uint32
	ResumeTimeout     time.Duration
}

type SnapshotDevice struct {
	Name   string `json:"name"`
	Input  string `json:"input"`
	Output string `json:"output"`
}
