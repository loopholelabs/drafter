package utils

import (
	"io"
)

type SectionReaderAt struct {
	r    io.ReaderAt
	off  int64
	size int64
}

func NewSectionReaderAt(
	r io.ReaderAt,
	off int64,
	size int64,
) *SectionReaderAt {
	return &SectionReaderAt{
		r,
		off,
		size,
	}
}

func (r *SectionReaderAt) ReadAt(p []byte, off int64) (n int, err error) {
	absoluteOff := r.off + off

	if absoluteOff < r.off {
		return 0, io.EOF
	}

	if absoluteOff > r.off+r.size {
		return 0, io.EOF
	}

	if (absoluteOff + int64(len(p))) > r.off+r.size {
		n, err = r.r.ReadAt(p[:(r.off+r.size)-absoluteOff], absoluteOff)
		if err != nil {
			return n, err
		}

		return n, io.EOF
	}

	return r.r.ReadAt(p, absoluteOff)
}
