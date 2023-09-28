package utils

import (
	"io"
	"os"
)

func CopyFile(src, dst string, uid int, gid int) (int64, error) {
	srcFile, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	defer dstFile.Close()

	n, err := io.Copy(dstFile, srcFile)
	if err != nil {
		return 0, err
	}

	if err := os.Chown(dst, uid, gid); err != nil {
		return 0, err
	}

	return n, nil
}
