package task

import (
	"sync"
	"context"
)
// Package work manages a pool of goroutines to perform work.

// Worker must be implemented by types that want to use the work pool.
type Worker interface {
	Task(context.Context)
}

// Pool provides a pool of goroutines that can execute any Worker
// tasks that are submitted.
type Pool struct {
	work chan Worker
	wg   sync.WaitGroup
}

// NewWorker creates a new work pool.
func NewWorker(ctx context.Context, maxGoroutines int) *Pool {
	p := Pool{
		work: make(chan Worker),
	}

	p.wg.Add(maxGoroutines)
	for i := 0; i < maxGoroutines; i++ {
		go func(context.Context) {
			for w := range p.work {
				w.Task(ctx)
			}
			p.wg.Done()
		}(ctx)
	}

	return &p
}

// Run submits work to the pool.
func (p *Pool) Run(w Worker) {
	p.work <- w
}

// Shutdown waits for all the goroutines to shutdown.
func (p *Pool) Shutdown() {
	close(p.work)
	p.wg.Wait()
}
