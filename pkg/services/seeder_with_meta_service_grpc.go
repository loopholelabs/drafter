package services

import (
	"context"

	v1 "github.com/loopholelabs/drafter/pkg/api/proto/migration/v1"
)

//go:generate sh -c "mkdir -p ../api/proto/migration/v1 && protoc --go_out=../api/proto/migration/v1 --go_opt=paths=source_relative --go-grpc_out=../api/proto/migration/v1 --go-grpc_opt=paths=source_relative --proto_path=../../api/proto/migration/v1 ../../api/proto/migration/v1/*.proto"

type SeederWithMetaServiceGrpc struct {
	v1.UnimplementedSeederWithMetaServer

	svc *SeederWithMetaService
}

func NewSeederWithMetaServiceGrpc(svc *SeederWithMetaService) *SeederWithMetaServiceGrpc {
	return &SeederWithMetaServiceGrpc{v1.UnimplementedSeederWithMetaServer{}, svc}
}

func (s *SeederWithMetaServiceGrpc) ReadAt(ctx context.Context, args *v1.ReadAtArgs) (*v1.ReadAtReply, error) {
	res, err := s.svc.ReadAt(ctx, int(args.GetLength()), args.GetOff())
	if err != nil {
		return nil, err
	}

	return &v1.ReadAtReply{
		N: int32(res.N),
		P: res.P,
	}, nil
}

func (s *SeederWithMetaServiceGrpc) Track(ctx context.Context, args *v1.TrackArgs) (*v1.TrackReply, error) {
	if err := s.svc.Track(ctx); err != nil {
		return nil, err
	}

	return &v1.TrackReply{}, nil
}

func (s *SeederWithMetaServiceGrpc) Sync(ctx context.Context, args *v1.SyncArgs) (*v1.SyncReply, error) {
	dirtyOffsets, err := s.svc.Sync(ctx)
	if err != nil {
		return nil, err
	}

	return &v1.SyncReply{
		DirtyOffsets: dirtyOffsets,
	}, nil
}

func (s *SeederWithMetaServiceGrpc) Close(ctx context.Context, args *v1.CloseArgs) (*v1.CloseReply, error) {
	if err := s.svc.Close(ctx); err != nil {
		return nil, err
	}

	return &v1.CloseReply{}, nil
}

func (s *SeederWithMetaServiceGrpc) Meta(ctx context.Context, args *v1.MetaArgs) (*v1.MetaReply, error) {
	size, agentVSockPort, err := s.svc.Meta(ctx)
	if err != nil {
		return nil, err
	}

	return &v1.MetaReply{
		Size:           size,
		AgentVSockPort: agentVSockPort,
	}, nil
}
