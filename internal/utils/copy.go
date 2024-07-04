package utils

import (
	"errors"
	"io"
	"os"
)

var (
	ErrCouldNotOpenSourceFile        = errors.New("could not open source file")
	ErrCouldNotCreateDestinationFile = errors.New("could not create destination file")
	ErrCouldNotCopyFileContent       = errors.New("could not copy file content")
	ErrCouldNotChangeFileOwner       = errors.New("could not change file owner")
)

func CopyFile(src, dst string, uid int, gid int) (int64, error) {
	srcFile, err := os.Open(src)
	if err != nil {
		return 0, errors.Join(ErrCouldNotOpenSourceFile, err)
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return 0, errors.Join(ErrCouldNotCreateDestinationFile, err)
	}
	defer dstFile.Close()

	n, err := io.Copy(dstFile, srcFile)
	if err != nil {
		return 0, errors.Join(ErrCouldNotCopyFileContent, err)
	}

	if err := os.Chown(dst, uid, gid); err != nil {
		return 0, errors.Join(ErrCouldNotChangeFileOwner, err)
	}

	return n, nil
}
