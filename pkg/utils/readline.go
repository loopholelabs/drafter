package utils

import "io"

func ReadLineNoBuffer(conn io.Reader) (string, error) {
	line := ""
	for {
		buf := make([]byte, 1)
		if _, err := conn.Read(buf); err != nil {
			return "", err
		}

		if buf[0] == '\n' {
			break
		}

		line += string(buf[0])
	}

	return line, nil
}
