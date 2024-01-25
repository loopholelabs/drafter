package remotes

import (
	"context"
	"io"

	"github.com/pojntfx/r3map/pkg/services"
)

type SeederWithAlignedReadsRemote struct {
	remote *services.SeederRemote

	rawSize     int64
	alignedSize int64
}

func NewSeederWithAlignedReadsRemote(
	remote *services.SeederRemote,

	rawSize int64,
	alignedSize int64,
) *services.SeederRemote {
	l := &SeederWithAlignedReadsRemote{
		remote: remote,

		rawSize:     rawSize,
		alignedSize: alignedSize,
	}

	return &services.SeederRemote{
		ReadAt: l.ReadAt,
		Track:  l.Track,
		Sync:   l.Sync,
		Close:  l.Close,
	}
}

func (l *SeederWithAlignedReadsRemote) ReadAt(ctx context.Context, length int, off int64) (r services.ReadAtResponse, err error) {
	if off >= l.alignedSize {
		return r, io.EOF
	}

	end := off + int64(length)
	if end <= l.rawSize {
		return l.remote.ReadAt(ctx, length, off)
	}

	var availableBytes int
	if off < l.rawSize {
		readLength := int(l.rawSize - off)
		r, err = l.remote.ReadAt(ctx, readLength, off)
		if err != nil {
			return r, err
		}
		availableBytes = len(r.P)
	}

	if end > l.alignedSize {
		end = l.alignedSize
		err = io.EOF
	}

	paddingSize := int(end - l.rawSize)
	paddedData := make([]byte, availableBytes+paddingSize)
	copy(paddedData, r.P)

	for i := availableBytes; i < len(paddedData); i++ {
		paddedData[i] = 0
	}

	r.P = paddedData

	return r, err
}

func (l *SeederWithAlignedReadsRemote) Track(ctx context.Context) error {
	return l.remote.Track(ctx)
}

func (l *SeederWithAlignedReadsRemote) Sync(ctx context.Context) ([]int64, error) {
	return l.remote.Sync(ctx)
}

func (l *SeederWithAlignedReadsRemote) Close(ctx context.Context) error {
	return l.remote.Close(ctx)
}
