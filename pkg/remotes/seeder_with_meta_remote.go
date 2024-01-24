package remotes

import (
	"context"

	"github.com/pojntfx/r3map/pkg/services"
)

type SeederWithMetaRemote struct {
	ReadAt func(context context.Context, length int, off int64) (r services.ReadAtResponse, err error)
	Track  func(context context.Context) error
	Sync   func(context context.Context) ([]int64, error)
	Close  func(context context.Context) error
	Meta   func(context context.Context) (size int64, err error)
}
