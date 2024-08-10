package utils

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

type PanicHandler struct {
	InternalCtx context.Context

	HandlePanics          func(track bool) func()
	HandleGoroutinePanics func(track bool, fn func())

	Cancel func()
	Wait   func()

	ErrFinishedType error
}

type GetPanicHandlerHooks struct {
	OnAfterRecover func()
}

func NewPanicHandler(
	ctx context.Context,

	errs *error,

	hooks GetPanicHandlerHooks,
) *PanicHandler {
	var (
		errsLock sync.Mutex
		wg       sync.WaitGroup
	)

	internalCtx, cancelInternalCtx := context.WithCancelCause(ctx)

	errFinished := errors.New("finished") // This has to be a distinct error type for each panic handler, so we can't define it on the package level

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

				if !(errors.Is(e, context.Canceled) && errors.Is(context.Cause(internalCtx), errFinished)) {
					*errs = errors.Join(*errs, e)

					if hook := hooks.OnAfterRecover; hook != nil {
						hook()
					}
				}

				cancelInternalCtx(errFinished)
			}
		}
	}

	return &PanicHandler{
		internalCtx,

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
			cancelInternalCtx(errFinished)
		},
		wg.Wait,

		errFinished,
	}
}
