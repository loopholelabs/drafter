package services

import (
	"context"
	"log"

	"github.com/pojntfx/go-nbd/pkg/backend"
	"github.com/pojntfx/r3map/pkg/services"
)

type SeederWithSizeService struct {
	base    *services.SeederService
	b       backend.Backend
	verbose bool
}

func NewSeederWithSizeService(
	base *services.SeederService,
	b backend.Backend,
	verbose bool,
) *SeederWithSizeService {
	return &SeederWithSizeService{base, b, verbose}
}

func (b *SeederWithSizeService) ReadAt(context context.Context, length int, off int64) (r services.ReadAtResponse, err error) {
	return b.base.ReadAt(context, length, off)
}

func (b *SeederWithSizeService) Track(context context.Context) error {
	return b.base.Track(context)
}

func (b *SeederWithSizeService) Sync(context context.Context) ([]int64, error) {
	return b.base.Sync(context)
}

func (b *SeederWithSizeService) Close(context context.Context) error {
	return b.base.Close(context)
}

func (b *SeederWithSizeService) Size(context context.Context) (int64, error) {
	if b.verbose {
		log.Println("Size()")
	}

	return b.b.Size()
}
