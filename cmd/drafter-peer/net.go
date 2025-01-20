package main

import (
	"context"
	"io"
	"net"
)

func connectAddr(ctx context.Context, addr string) (io.Closer, []io.Reader, []io.Writer, string, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, nil, nil, "", err
	}
	readers := []io.Reader{conn}
	writers := []io.Writer{conn}
	return conn, readers, writers, conn.RemoteAddr().String(), nil
}

func listenAddr(ctx context.Context, addr string) (io.Closer, []io.Reader, []io.Writer, string, error) {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, nil, "", err
	}
	defer lis.Close()

	conn, err := lis.Accept()
	if err != nil {
		return nil, nil, nil, "", err
	}

	readers := []io.Reader{conn}
	writers := []io.Writer{conn}
	return conn, readers, writers, conn.RemoteAddr().String(), nil
}
