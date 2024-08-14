package packager

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"os"

	"github.com/klauspost/compress/zstd"
)

type PackagerDevice struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type PackagerHooks struct {
	OnBeforeProcessFile func(name, path string)
}

func ArchivePackage(
	ctx context.Context,

	devices []PackagerDevice,
	packageOutputPath string,

	hooks PackagerHooks,
) error {
	packageOutputFile, err := os.OpenFile(packageOutputPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.ModePerm)
	if err != nil {
		return errors.Join(ErrCouldNotOpenPackageOutputFile, err)
	}
	defer packageOutputFile.Close()

	compressor, err := zstd.NewWriter(packageOutputFile)
	if err != nil {
		return errors.Join(ErrCouldNotCreateCompressor, err)
	}
	defer compressor.Close()

	packageOutputArchive := tar.NewWriter(compressor)
	defer packageOutputArchive.Close()

	for _, device := range devices {
	s:
		select {
		case <-ctx.Done():
			return ctx.Err()

		default:
			break s
		}

		if hook := hooks.OnBeforeProcessFile; hook != nil {
			hook(device.Name, device.Path)
		}

		info, err := os.Stat(device.Path)
		if err != nil {
			return errors.Join(ErrCouldNotStatDevice, err)
		}

		header, err := tar.FileInfoHeader(info, device.Path)
		if err != nil {
			return errors.Join(ErrCouldNotCreateTarHeader, err)
		}
		header.Name = device.Name

		if err := packageOutputArchive.WriteHeader(header); err != nil {
			return errors.Join(ErrCouldNotWriteTarHeader, err)
		}

		f, err := os.Open(device.Path)
		if err != nil {
			return errors.Join(ErrCouldNotOpenDevice, err)
		}
		defer f.Close()

		if _, err = io.Copy(packageOutputArchive, f); err != nil {
			return errors.Join(ErrCouldNotCopyToArchive, err)
		}
	}

	return nil
}
