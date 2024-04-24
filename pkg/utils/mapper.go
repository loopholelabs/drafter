package utils

import (
	"errors"
	"sync"
)

func ConcurrentMap[I, O any](
	input []I,
	callback func(index int, input I, output *O, addDefer func(deferFunc func() error)) error,
) ([]O, [][]func() error, error) {
	var (
		wg sync.WaitGroup

		outputs     = []O{}
		outputsLock sync.Mutex

		defers     = [][]func() error{}
		defersLock sync.Mutex

		errs     error
		errsLock sync.Mutex
	)
	for i, input := range input {
		wg.Add(1)

		go func(i int, input I) {
			var (
				localDefers     = []func() error{}
				localDefersLock sync.Mutex
			)

			err := func() error {
				output := new(O)
				defer func() {
					defer wg.Done()

					outputsLock.Lock()
					defer outputsLock.Unlock()

					outputs = append(outputs, *output)

					defersLock.Lock()
					defer defersLock.Unlock()

					defers = append(defers, localDefers)
				}()

				return callback(i, input, output, func(deferFunc func() error) {
					localDefersLock.Lock()
					defer localDefersLock.Unlock()

					localDefers = append(localDefers, deferFunc)
				})
			}()

			errsLock.Lock()
			defer errsLock.Unlock()

			errs = errors.Join(errs, err)
		}(i, input)
	}

	wg.Wait()

	return outputs, defers, errs
}
