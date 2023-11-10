package remotes

import "context"

type ManagerRemote struct {
	Register func(ctx context.Context, name string) error
}
