package packager

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/klauspost/compress/zstd"
)

func ExtractPackage(
	ctx context.Context,

	packageInputPath string,
	devices []PackagerDevice,

	hooks PackagerHooks,
) error {
	packageFile, err := os.Open(packageInputPath)
	if err != nil {
		return errors.Join(ErrCouldNotOpenPackageInputFile, err)
	}
	defer packageFile.Close()

	uncompressor, err := zstd.NewReader(packageFile)
	if err != nil {
		return errors.Join(ErrCouldNotCreateUncompressor, err)
	}
	defer uncompressor.Close()

	packageArchive := tar.NewReader(uncompressor)

	for _, device := range devices {
		extracted := false
		for {
		s:
			select {
			case <-ctx.Done():
				return ctx.Err()

			default:
				break s
			}

			header, err := packageArchive.Next()
			if err != nil {
				if err == io.EOF {
					break
				}

				return errors.Join(ErrCouldNotReadNextHeader, err)
			}

			if header.Name != device.Name {
				continue
			}

			if hook := hooks.OnBeforeProcessFile; hook != nil {
				hook(device.Name, device.Path)
			}

			if err := os.MkdirAll(filepath.Dir(device.Path), os.ModePerm); err != nil {
				return errors.Join(ErrCouldNotCreateOutputDir, err)
			}

			outputFile, err := os.OpenFile(device.Path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.ModePerm)
			if err != nil {
				return errors.Join(ErrCouldNotOpenOutputFile, err)
			}
			defer outputFile.Close()

			if _, err = io.Copy(outputFile, packageArchive); err != nil {
				return errors.Join(ErrCouldNotCopyToOutput, err)
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
