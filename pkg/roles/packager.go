package roles

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/klauspost/compress/zstd"
)

var (
	ErrMissingDevice = errors.New("missing resource")
)

type PackagerDevice struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type PackagerHooks struct {
	OnBeforeProcessFile func(name string)
}

func ArchivePackage(
	devices []PackagerDevice,
	packageOutputPath string,

	hooks PackagerHooks,
) error {
	packageOutputFile, err := os.OpenFile(packageOutputPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.ModePerm)
	if err != nil {
		return err
	}
	defer packageOutputFile.Close()

	compressor, err := zstd.NewWriter(packageOutputFile)
	if err != nil {
		return err
	}
	defer compressor.Close()

	packageOutputArchive := tar.NewWriter(compressor)
	defer packageOutputArchive.Close()

	for _, device := range devices {
		if hook := hooks.OnBeforeProcessFile; hook != nil {
			hook(device.Name)
		}

		info, err := os.Stat(device.Path)
		if err != nil {
			return err
		}

		header, err := tar.FileInfoHeader(info, device.Path)
		if err != nil {
			return err
		}
		header.Name = device.Name

		if err := packageOutputArchive.WriteHeader(header); err != nil {
			return err
		}

		f, err := os.Open(device.Path)
		if err != nil {
			return err
		}
		defer f.Close()

		if _, err = io.Copy(packageOutputArchive, f); err != nil {
			return err
		}
	}

	return nil
}

func ExtractPackage(
	packageInputPath string,
	devices []PackagerDevice,

	hooks PackagerHooks,
) error {
	packageFile, err := os.Open(packageInputPath)
	if err != nil {
		return err
	}
	defer packageFile.Close()

	uncompressor, err := zstd.NewReader(packageFile)
	if err != nil {
		return err
	}
	defer uncompressor.Close()

	packageArchive := tar.NewReader(uncompressor)

	for _, device := range devices {
		extracted := false
		for {
			header, err := packageArchive.Next()
			if err != nil {
				if err == io.EOF {
					break
				}

				return err
			}

			if header.Name != device.Name {
				continue
			}

			if hook := hooks.OnBeforeProcessFile; hook != nil {
				hook(device.Name)
			}

			if err := os.MkdirAll(filepath.Dir(device.Path), os.ModePerm); err != nil {
				return err
			}

			outputFile, err := os.OpenFile(device.Path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.ModePerm)
			if err != nil {
				return err
			}
			defer outputFile.Close()

			if _, err = io.Copy(outputFile, packageArchive); err != nil {
				return err
			}

			extracted = true

			break
		}

		if !extracted {
			// We join the more specific error here first
			return errors.Join(fmt.Errorf("missing device: %s", device.Name), ErrMissingDevice)
		}
	}

	return nil
}
