package utils

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
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

func ReadPackageConfigFromTar(archive *tar.Reader) (*PackageConfig, fs.FileInfo, error) {
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}

		if header.Name == PackageConfigName {
			var packageConfig PackageConfig
			if err = json.NewDecoder(archive).Decode(&packageConfig); err != nil {
				return nil, nil, err
			}

			return &packageConfig, header.FileInfo(), nil
		}
	}

	return nil, nil, ErrPackageConfigFileNotFound
}
