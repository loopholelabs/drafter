package utils

import (
	"os"
)

func WriteFile(content []byte, dst string, uid int, gid int) (int, error) {
	dstFile, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	defer dstFile.Close()

	n, err := dstFile.Write(content)
	if err != nil {
		return 0, err
	}

	if err := os.Chown(dst, uid, gid); err != nil {
		return 0, err
	}

	return n, nil
}
