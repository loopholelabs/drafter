package services

import (
	"context"
	"log"

	"github.com/pojntfx/go-nbd/pkg/backend"
	"github.com/pojntfx/r3map/pkg/services"
)

type SeederWithMetaService struct {
	base           *services.SeederService
	b              backend.Backend
	agentVSockPort uint32
	verbose        bool
}

func NewSeederWithMetaService(
	base *services.SeederService,
	b backend.Backend,
	agentVSockPort uint32,
	verbose bool,
) *SeederWithMetaService {
	return &SeederWithMetaService{base, b, agentVSockPort, verbose}
}

func (b *SeederWithMetaService) ReadAt(context context.Context, length int, off int64) (r services.ReadAtResponse, err error) {
	return b.base.ReadAt(context, length, off)
}

func (b *SeederWithMetaService) Track(context context.Context) error {
	return b.base.Track(context)
}

func (b *SeederWithMetaService) Sync(context context.Context) ([]int64, error) {
	return b.base.Sync(context)
}

func (b *SeederWithMetaService) Close(context context.Context) error {
	return b.base.Close(context)
}

func (b *SeederWithMetaService) Meta(context context.Context) (size int64, agentVSockPort uint32, err error) {
	if b.verbose {
		log.Println("Meta()")
	}

	size, err = b.b.Size()
	if err != nil {
		return 0, 0, err
	}

	return size, b.agentVSockPort, nil
}
