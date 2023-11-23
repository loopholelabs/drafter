package utils

import (
	"sync"
)

func ConcurrentMap[I, O any](
	input []I,
	callback func(index int, input I, output *O, addDefer func(deferFunc func() error)) error,
) ([]O, []func() error, []error) {
	var (
		outputsWg sync.WaitGroup

		outputs     = []O{}
		outputsLock sync.Mutex

		defers     = []func() error{}
		defersLock sync.Mutex

		errs     = []error{}
		errsLock sync.Mutex
	)
	for i, input := range input {
		outputsWg.Add(1)

		go func(i int, input I) {
			err := func() error {
				output := new(O)
				defer func() {
					defer outputsWg.Done()

					outputsLock.Lock()
					defer outputsLock.Unlock()

					outputs = append(outputs, *output)
				}()

				return callback(i, input, output, func(deferFunc func() error) {
					defersLock.Lock()
					defer defersLock.Unlock()

					defers = append(defers, deferFunc)
				})
			}()

			errsLock.Lock()
			defer errsLock.Unlock()

			errs = append(errs, err)
		}(i, input)
	}

	outputsWg.Wait()

	return outputs, defers, errs
}
