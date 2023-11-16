package backend

import (
	"errors"
	"io"
	"sync"

	"github.com/nlepage/go-tarfs"
)

var (
	ErrInvalidFile   = errors.New("invalid file")
	ErrUnimplemented = errors.New("unimplemented")
)

type TarBackend struct {
	reader io.Reader
	path   string
	file   io.Reader
	seeker io.Seeker
	size   int64
	lock   sync.Mutex
}

func NewTarBackend(r io.Reader, path string) *TarBackend {
	return &TarBackend{
		r,
		path,
		nil,
		nil,
		0,
		sync.Mutex{},
	}
}

func (b *TarBackend) Open() (err error) {
	fs, err := tarfs.New(b.reader)
	if err != nil {
		return err
	}

	f, err := fs.Open(b.path)
	if err != nil {
		return err
	}
	b.file = f

	s, ok := f.(io.Seeker)
	if !ok {
		return ErrInvalidFile
	}
	b.seeker = s

	stat, err := f.Stat()
	if err != nil {
		return err
	}
	b.size = stat.Size()

	return nil
}

func (b *TarBackend) ReadAt(p []byte, off int64) (n int, err error) {
	b.lock.Lock()

	m, err := b.seeker.Seek(off, io.SeekStart)
	if err != nil {
		b.lock.Unlock()

		return int(m), err
	}

	n, err = b.file.Read(p)

	b.lock.Unlock()

	return
}

func (b *TarBackend) WriteAt(p []byte, off int64) (n int, err error) {
	return 0, ErrUnimplemented
}

func (b *TarBackend) Size() (int64, error) {
	return b.size, nil
}

func (b *TarBackend) Sync() error {
	return nil
}
