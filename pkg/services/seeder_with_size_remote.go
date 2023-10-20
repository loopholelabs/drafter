package services

import (
	"context"

	"github.com/pojntfx/r3map/pkg/services"
)

type SeederWithSizeRemote struct {
	ReadAt func(context context.Context, length int, off int64) (r services.ReadAtResponse, err error)
	Track  func(context context.Context) error
	Sync   func(context context.Context) ([]int64, error)
	Close  func(context context.Context) error
	Size   func(context context.Context) (int64, error)
}
