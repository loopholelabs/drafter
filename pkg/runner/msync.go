package runner

import (
	"context"
	"errors"

	"github.com/loopholelabs/drafter/internal/firecracker"
	"github.com/loopholelabs/drafter/pkg/snapshotter"
)

func (resumedRunner *ResumedRunner[L, R, G]) Msync(ctx context.Context) error {
	if !resumedRunner.snapshotLoadConfiguration.ExperimentalMapPrivate {
		if err := firecracker.CreateSnapshot(
			ctx,

			resumedRunner.runner.firecrackerClient,

			resumedRunner.runner.stateName,
			"",

			firecracker.SnapshotTypeMsync,
		); err != nil {
			return errors.Join(snapshotter.ErrCouldNotCreateSnapshot, err)
		}
	}

	return nil
}
