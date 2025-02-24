package fc_tests

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path"
	"testing"

	"github.com/loopholelabs/logging"
	"github.com/stretchr/testify/assert"
)

/**
 * Pre-requisites
 *  - ark0 network namespace exists
 *  - firecracker works
 */
func TestSnapshotter(t *testing.T) {
	currentUser, err := user.Current()
	if err != nil {
		panic(err)
	}
	if currentUser.Username != "root" {
		fmt.Printf("Cannot run test unless we are root.\n")
		return
	}

	log := logging.New(logging.Zerolog, "test", os.Stderr)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	snapDir := setupSnapshot(t, log, ctx)

	// Check output

	for _, n := range []string{"state", "memory", "kernel", "disk", "config", "oci"} {
		s, err := os.Stat(path.Join(snapDir, n))
		assert.NoError(t, err)

		fmt.Printf("Output %s filesize %d\n", n, s.Size())
		assert.Greater(t, s.Size(), int64(0))
	}
}
