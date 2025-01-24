package common

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

var tempPackageDir = "test_package"

func TestPackage(t *testing.T) {
	err := os.Mkdir(tempPackageDir, 0777)
	assert.NoError(t, err)

	t.Cleanup(func() {
		err := os.RemoveAll(tempPackageDir)
		assert.NoError(t, err)
	})

	// TODO: Create some random files, package them, unpackage, assert they're the same.

}
