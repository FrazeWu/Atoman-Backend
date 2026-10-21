// Package searchlimit shares the database query budget between reference and unified search.
package searchlimit

import "context"

const Capacity = 8

var slots = make(chan struct{}, Capacity)

func Acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case slots <- struct{}{}:
		if err := ctx.Err(); err != nil {
			Release()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func Release() { <-slots }
