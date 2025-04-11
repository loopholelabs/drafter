package testutil

import (
	"os/exec"
)

func FirecrackerAvailable() bool {
	_, err := exec.LookPath("firecracker")
	if err != nil {
		return false
	}
	return true
}
