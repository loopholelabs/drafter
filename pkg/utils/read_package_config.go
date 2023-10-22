package utils

import (
	"encoding/json"
	"errors"
	"io"

	"github.com/dsoprea/go-ext4"
)

const (
	PackageConfigName = "architekt.arkconfig"
)

var (
	ErrPackageConfigFileNotFound = errors.New("package config file not found")
)

type PackageConfig struct {
	AgentVSockPort uint32 `json:"agentVSockPort"`
}

func ReadPackageConfigFromEXT4Filesystem(fs io.ReadSeeker) (PackageConfig, error) {
	if _, err := fs.Seek(ext4.Superblock0Offset, io.SeekStart); err != nil {
		return PackageConfig{}, err
	}

	superblock, err := ext4.NewSuperblockWithReader(fs)
	if err != nil {
		return PackageConfig{}, err
	}

	blockGroupDescriptors, err := ext4.NewBlockGroupDescriptorListWithReadSeeker(fs, superblock)
	if err != nil {
		return PackageConfig{}, err
	}

	rootBlockGroup, err := blockGroupDescriptors.GetWithAbsoluteInode(ext4.InodeRootDirectory)
	if err != nil {
		return PackageConfig{}, err
	}

	rootDirectory, err := ext4.NewDirectoryWalk(fs, rootBlockGroup, ext4.InodeRootDirectory)
	if err != nil {
		return PackageConfig{}, err
	}

	var entry *ext4.DirectoryEntry
	for {
		candidatePath, candidateEntry, err := rootDirectory.Next()
		if err != nil {
			if err == io.EOF {
				break
			}

			return PackageConfig{}, err
		}

		if candidatePath == PackageConfigName {
			entry = candidateEntry

			break
		}
	}

	if entry == nil {
		return PackageConfig{}, ErrPackageConfigFileNotFound
	}

	packageConfigInode, err := ext4.NewInodeWithReadSeeker(rootBlockGroup, fs, int(entry.Data().Inode))
	if err != nil {
		return PackageConfig{}, err
	}

	var packageConfig PackageConfig
	if err := json.NewDecoder(
		ext4.NewInodeReader(
			ext4.NewExtentNavigatorWithReadSeeker(fs, packageConfigInode),
		),
	).Decode(&packageConfig); err != nil {
		return PackageConfig{}, err
	}

	return packageConfig, nil
}
