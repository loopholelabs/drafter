package utils

import (
	"os"
)

func WriteFile(content []byte, output string, uid int, gid int) (int, error) {
	outputFile, err := os.OpenFile(output, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.ModePerm)
	if err != nil {
		return 0, err
	}
	defer outputFile.Close()

	n, err := outputFile.Write(content)
	if err != nil {
		return 0, err
	}

	if err := os.Chown(output, uid, gid); err != nil {
		return 0, err
	}

	return n, nil
}
