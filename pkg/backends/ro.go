package backend

import (
	"errors"
	"io"
	"sync"
)

var (
	ErrUnimplemented = errors.New("unimplemented")
)

type ReadOnlyBackend struct {
	r    io.ReaderAt
	size int64

	lock sync.Mutex
}

func NewReadOnlyBackend(r io.ReaderAt, size int64) *ReadOnlyBackend {
	return &ReadOnlyBackend{
		r,
		size,

		sync.Mutex{},
	}
}

func (b *ReadOnlyBackend) ReadAt(p []byte, off int64) (n int, err error) {
	return b.r.ReadAt(p, off)
}

func (b *ReadOnlyBackend) WriteAt(p []byte, off int64) (n int, err error) {
	return 0, ErrUnimplemented
}

func (b *ReadOnlyBackend) Size() (int64, error) {
	return b.size, nil
}

func (b *ReadOnlyBackend) Sync() error {
	return nil
}
