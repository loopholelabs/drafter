package utils

import (
	"archive/tar"
	"errors"
	"io"
)

var (
	ErrPathNotFound = errors.New("path not found")
)

func FindSectionForPathInArchive(
	packageFile io.ReadSeeker,
	path string,
) (off, size int64, err error) {
	packageArchive := tar.NewReader(packageFile)

	for {
		header, err := packageArchive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, 0, err
		}

		if header.Name == path {
			curr, err := packageFile.Seek(0, io.SeekCurrent)
			if err != nil {
				return 0, 0, err
			}

			return curr, header.Size, nil
		}
	}

	return 0, 0, ErrPathNotFound
}
