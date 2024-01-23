package roles

import (
	"archive/tar"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/loopholelabs/drafter/pkg/config"
)

var (
	ErrMissingInitramfs = errors.New("missing initramfs")
	ErrMissingKernel    = errors.New("missing kernel")
	ErrMissingDisk      = errors.New("missing disk")
	ErrMissingState     = errors.New("missing state")
	ErrMissingMemory    = errors.New("missing memory")
	ErrMissingConfig    = errors.New("missing config")
)

type resource struct {
	name string
	path string
	err  error
}

func ArchivePackage(
	stateInputPath string,
	memoryInputPath string,
	initramfsInputPath string,
	kernelInputPath string,
	diskInputPath string,
	configInputPath string,

	packageOutputPath string,

	knownNamesConfiguration config.KnownNamesConfiguration,
) error {
	resources := []resource{
		{
			name: knownNamesConfiguration.InitramfsName,
			path: initramfsInputPath,
		},
		{
			name: knownNamesConfiguration.KernelName,
			path: kernelInputPath,
		},
		{
			name: knownNamesConfiguration.DiskName,
			path: diskInputPath,
		},

		{
			name: knownNamesConfiguration.StateName,
			path: stateInputPath,
		},
		{
			name: knownNamesConfiguration.MemoryName,
			path: memoryInputPath,
		},

		{
			name: knownNamesConfiguration.ConfigName,
			path: configInputPath,
		},
	}

	packageOutputFile, err := os.OpenFile(packageOutputPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.ModePerm)
	if err != nil {
		return err
	}
	defer packageOutputFile.Close()

	packageOutputArchive := tar.NewWriter(packageOutputFile)
	defer packageOutputArchive.Close()

	for _, resource := range resources {
		info, err := os.Stat(resource.path)
		if err != nil {
			return err
		}

		header, err := tar.FileInfoHeader(info, resource.path)
		if err != nil {
			return err
		}
		header.Name = resource.name

		if err := packageOutputArchive.WriteHeader(header); err != nil {
			return err
		}

		f, err := os.Open(resource.path)
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

	stateOutputPath string,
	memoryOutputPath string,
	initramfsOutputPath string,
	kernelOutputPath string,
	diskOutputPath string,
	configOutputPath string,

	knownNamesConfiguration config.KnownNamesConfiguration,
) error {
	resources := []resource{
		{
			name: knownNamesConfiguration.InitramfsName,
			path: initramfsOutputPath,
			err:  ErrMissingInitramfs,
		},
		{
			name: knownNamesConfiguration.KernelName,
			path: kernelOutputPath,
			err:  ErrMissingKernel,
		},
		{
			name: knownNamesConfiguration.DiskName,
			path: diskOutputPath,
			err:  ErrMissingDisk,
		},

		{
			name: knownNamesConfiguration.StateName,
			path: stateOutputPath,
			err:  ErrMissingState,
		},
		{
			name: knownNamesConfiguration.MemoryName,
			path: memoryOutputPath,
			err:  ErrMissingMemory,
		},

		{
			name: knownNamesConfiguration.ConfigName,
			path: configOutputPath,
			err:  ErrMissingConfig,
		},
	}

	packageFile, err := os.Open(packageInputPath)
	if err != nil {
		return err
	}
	defer packageFile.Close()

	packageArchive := tar.NewReader(packageFile)

	for _, resource := range resources {
		extracted := false
		for {
			header, err := packageArchive.Next()
			if err != nil {
				if err == io.EOF {
					break
				}

				return err
			}

			if header.Name != resource.name {
				continue
			}

			if err := os.MkdirAll(filepath.Dir(resource.path), os.ModePerm); err != nil {
				return err
			}

			outputFile, err := os.OpenFile(resource.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.ModePerm)
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
			return resource.err
		}
	}

	return nil
}
