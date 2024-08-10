package utils

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

type GoroutineManagerHooks struct {
	OnAfterRecover func()
}

type GoroutineManager struct {
	errsLock *sync.Mutex
	wg       *sync.WaitGroup

	goroutineCtx      context.Context
	cancelInternalCtx context.CancelCauseFunc

	errFinished error

	recoverFromPanics func(track bool) func()

	errGoroutineStopped error
}

func NewGoroutineManager(
	ctx context.Context,

	errs *error,

	hooks GoroutineManagerHooks,
) *GoroutineManager {
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

	return &GoroutineManager{
		&errsLock,
		&wg,

		internalCtx,
		cancelInternalCtx,

		errFinished,

		recoverFromPanics,

		errFinished,
	}
}

func (m *GoroutineManager) CreateForegroundPanicCollector() func() {
	m.wg.Add(1)

	return m.recoverFromPanics(true)
}

func (m *GoroutineManager) CreateBackgroundPanicCollector() func() {
	return m.recoverFromPanics(false)
}

func (m *GoroutineManager) StartForegroundGoroutine(fn func()) {
	m.wg.Add(1)

	go func() {
		defer m.recoverFromPanics(true)()

		fn()
	}()
}

func (m *GoroutineManager) StartBackgroundGoroutine(fn func()) {
	go func() {
		defer m.recoverFromPanics(false)()

		fn()
	}()
}

func (m *GoroutineManager) StopAllGoroutines() {
	m.cancelInternalCtx(m.errFinished)
}

func (m *GoroutineManager) WaitForForegroundGoroutines() {
	m.wg.Wait()
}

func (m *GoroutineManager) GetGoroutineCtx() context.Context {
	return m.goroutineCtx
}

func (m *GoroutineManager) GetErrGoroutineStopped() error {
	return m.errGoroutineStopped
}
