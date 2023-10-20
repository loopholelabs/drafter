package services

import (
	"context"

	v1 "github.com/loopholelabs/architekt/pkg/api/proto/migration/v1"
	"github.com/pojntfx/r3map/pkg/services"
)

type SeederWithSizeRemoteGrpc struct {
	client v1.SeederWithSizeClient
}

func NewSeederWithSizeRemoteGrpc(
	client v1.SeederWithSizeClient,
) (*services.SeederRemote, *SeederWithSizeRemote) {
	l := &SeederWithSizeRemoteGrpc{client}

	return &services.SeederRemote{
			ReadAt: l.ReadAt,
			Track:  l.Track,
			Sync:   l.Sync,
			Close:  l.Close,
		}, &SeederWithSizeRemote{
			ReadAt: l.ReadAt,
			Track:  l.Track,
			Sync:   l.Sync,
			Close:  l.Close,
			Size:   l.Size,
		}
}

func (l *SeederWithSizeRemoteGrpc) ReadAt(ctx context.Context, length int, off int64) (r services.ReadAtResponse, err error) {
	res, err := l.client.ReadAt(ctx, &v1.ReadAtArgs{
		Length: int32(length),
		Off:    off,
	})
	if err != nil {
		return services.ReadAtResponse{}, err
	}

	return services.ReadAtResponse{
		N: int(res.GetN()),
		P: res.GetP(),
	}, err
}

func (l *SeederWithSizeRemoteGrpc) Track(ctx context.Context) error {
	if _, err := l.client.Track(ctx, &v1.TrackArgs{}); err != nil {
		return err
	}

	return nil
}

func (l *SeederWithSizeRemoteGrpc) Sync(ctx context.Context) ([]int64, error) {
	res, err := l.client.Sync(ctx, &v1.SyncArgs{})
	if err != nil {
		return []int64{}, err
	}

	return res.GetDirtyOffsets(), nil
}

func (l *SeederWithSizeRemoteGrpc) Close(ctx context.Context) error {
	if _, err := l.client.Close(ctx, &v1.CloseArgs{}); err != nil {
		return err
	}

	return nil
}

func (l *SeederWithSizeRemoteGrpc) Size(ctx context.Context) (int64, error) {
	res, err := l.client.Size(ctx, &v1.SizeArgs{})
	if err != nil {
		return 0, err
	}

	return res.GetSize(), nil
}
