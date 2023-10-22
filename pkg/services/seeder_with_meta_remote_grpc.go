package services

import (
	"context"

	v1 "github.com/loopholelabs/architekt/pkg/api/proto/migration/v1"
	"github.com/pojntfx/r3map/pkg/services"
)

type SeederWithMetaRemoteGrpc struct {
	client v1.SeederWithMetaClient
}

func NewSeederWithMetaRemoteGrpc(
	client v1.SeederWithMetaClient,
) (*services.SeederRemote, *SeederWithMetaRemote) {
	l := &SeederWithMetaRemoteGrpc{client}

	return &services.SeederRemote{
			ReadAt: l.ReadAt,
			Track:  l.Track,
			Sync:   l.Sync,
			Close:  l.Close,
		}, &SeederWithMetaRemote{
			ReadAt: l.ReadAt,
			Track:  l.Track,
			Sync:   l.Sync,
			Close:  l.Close,
			Meta:   l.Meta,
		}
}

func (l *SeederWithMetaRemoteGrpc) ReadAt(ctx context.Context, length int, off int64) (r services.ReadAtResponse, err error) {
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

func (l *SeederWithMetaRemoteGrpc) Track(ctx context.Context) error {
	if _, err := l.client.Track(ctx, &v1.TrackArgs{}); err != nil {
		return err
	}

	return nil
}

func (l *SeederWithMetaRemoteGrpc) Sync(ctx context.Context) ([]int64, error) {
	res, err := l.client.Sync(ctx, &v1.SyncArgs{})
	if err != nil {
		return []int64{}, err
	}

	return res.GetDirtyOffsets(), nil
}

func (l *SeederWithMetaRemoteGrpc) Close(ctx context.Context) error {
	if _, err := l.client.Close(ctx, &v1.CloseArgs{}); err != nil {
		return err
	}

	return nil
}

func (l *SeederWithMetaRemoteGrpc) Meta(ctx context.Context) (size int64, agentVSockPort uint32, err error) {
	res, err := l.client.Meta(ctx, &v1.MetaArgs{})
	if err != nil {
		return 0, 0, err
	}

	return res.GetSize(), res.GetAgentVSockPort(), nil
}
