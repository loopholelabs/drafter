package utils

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var (
	ErrFinished = errors.New("finished")
)

func GetPanicHandler(
	ctx context.Context,

	errs *error,
) (
	internalCtx context.Context,

	handlePanics func(track bool) func(),
	handleGoroutinePanics func(track bool, fn func()),

	cancel func(),
	wait func(),
) {
	var (
		errsLock sync.Mutex
		wg       sync.WaitGroup
	)

	internalCtx, cancelInternalCtx := context.WithCancelCause(ctx)

	recoverFromPanics := func(track bool) func() {
		return func() {
			if track {
				defer wg.Done()
			}

			if err := recover(); err != nil {
				errsLock.Lock()
				defer errsLock.Unlock()

				var e error
				if v, ok := err.(error); ok {
					e = v
				} else {
					e = fmt.Errorf("%v", err)
				}

				if !(errors.Is(e, context.Canceled) && errors.Is(context.Cause(internalCtx), ErrFinished)) {
					*errs = errors.Join(*errs, e)
				}

				cancelInternalCtx(ErrFinished)
			}
		}
	}

	return internalCtx,

		func(track bool) func() {
			if track {
				wg.Add(1)
			}

			return recoverFromPanics(track)
		},
		func(track bool, fn func()) {
			if track {
				wg.Add(1)
			}

			go func() {
				defer recoverFromPanics(track)()

				fn()
			}()
		},

		func() {
			cancelInternalCtx(ErrFinished)
		},
		wg.Wait
}
