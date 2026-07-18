package slurm

import (
	"context"
	"sync"
	"time"
)

type valueCache[T any] struct {
	mu          sync.Mutex
	ttl         time.Duration
	lastAttempt time.Time
	value       T
	err         error
	refreshing  chan struct{}
}

func (c *valueCache[T]) get(parent context.Context, loader func(context.Context) (T, error)) (T, error) {
	for {
		c.mu.Lock()
		if !c.lastAttempt.IsZero() && time.Since(c.lastAttempt) < c.ttl {
			value, err := c.value, c.err
			c.mu.Unlock()
			return value, err
		}
		if refresh := c.refreshing; refresh != nil {
			c.mu.Unlock()
			select {
			case <-parent.Done():
				var zero T
				return zero, parent.Err()
			case <-refresh:
				continue
			}
		}
		refresh := make(chan struct{})
		c.refreshing = refresh
		c.mu.Unlock()

		value, err := loader(parent)
		c.mu.Lock()
		if parent.Err() == nil {
			c.lastAttempt, c.value, c.err = time.Now(), value, err
		}
		c.refreshing = nil
		close(refresh)
		c.mu.Unlock()
		if parent.Err() != nil {
			var zero T
			return zero, parent.Err()
		}
		return value, err
	}
}
