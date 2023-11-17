package backend

import (
	"sync"

	"github.com/pojntfx/go-nbd/pkg/backend"
)

type PoolBackend struct {
	backends []backend.Backend
	lock     sync.Mutex
	cond     *sync.Cond
	busy     []bool
}

func NewPoolBackend(backends []backend.Backend) *PoolBackend {
	pb := &PoolBackend{
		backends: backends,
		busy:     make([]bool, len(backends)),
	}

	pb.cond = sync.NewCond(&pb.lock)

	return pb
}

func (b *PoolBackend) getFreeBackend() backend.Backend {
	b.lock.Lock()
	defer b.lock.Unlock()

	for {
		for i, isBusy := range b.busy {
			if !isBusy {
				b.busy[i] = true

				return b.backends[i]
			}
		}

		b.cond.Wait()
	}
}

func (b *PoolBackend) releaseBackend(backend backend.Backend) {
	b.lock.Lock()
	defer b.lock.Unlock()

	for i, be := range b.backends {
		if be == backend {
			b.busy[i] = false

			break
		}
	}

	b.cond.Broadcast()
}

func (b *PoolBackend) ReadAt(p []byte, off int64) (n int, err error) {
	be := b.getFreeBackend()
	defer b.releaseBackend(be)

	return be.ReadAt(p, off)
}

func (b *PoolBackend) WriteAt(p []byte, off int64) (n int, err error) {
	be := b.getFreeBackend()
	defer b.releaseBackend(be)

	return be.WriteAt(p, off)
}

func (b *PoolBackend) Size() (int64, error) {
	be := b.getFreeBackend()
	defer b.releaseBackend(be)

	return be.Size()
}

func (b *PoolBackend) Sync() error {
	be := b.getFreeBackend()
	defer b.releaseBackend(be)

	return be.Sync()
}
