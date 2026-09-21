package core

import (
	"context"
	"fmt"
	"sync"
)

// Scheduler enforces strict concurrency limits to prevent overloading local LLM VRAM/KV cache.
type Scheduler struct {
	sem chan struct{}
	wg  sync.WaitGroup
}

func NewScheduler(maxConcurrent int) *Scheduler {
	if maxConcurrent <= 0 {
		maxConcurrent = 2
	}
	return &Scheduler{
		sem: make(chan struct{}, maxConcurrent),
	}
}

// Submit enqueues and runs a task, respecting context cancellation while waiting for a worker slot.
func (s *Scheduler) Submit(ctx context.Context, taskFunc func(context.Context)) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("task canceled before acquiring execution slot: %w", ctx.Err())
	case s.sem <- struct{}{}:
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() { <-s.sem }()
		taskFunc(ctx)
	}()

	return nil
}

// Capacity returns the current active and total worker slots.
func (s *Scheduler) Stats() (active, total int) {
	return len(s.sem), cap(s.sem)
}

// Wait blocks until all currently executing tasks complete.
func (s *Scheduler) Wait() {
	s.wg.Wait()
}
