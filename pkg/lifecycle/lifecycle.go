package lifecycle

import (
	"context"
	"sync"
)

// possibly overkill for lifecycle management lol

type TaskSpawner func(func(ctx context.Context))

type Lifecycle struct {
	wg     sync.WaitGroup
	ctx    context.Context
	cancel context.CancelFunc
}

func NewLifecycle(parent context.Context) *Lifecycle {
	ctx, cancel := context.WithCancel(parent)
	return &Lifecycle{
		ctx:    ctx,
		cancel: cancel,
	}
}

func (l *Lifecycle) Go(fn func(ctx context.Context)) {
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		fn(l.ctx)
	}()
}

func (l *Lifecycle) Shutdown() {
	l.cancel()
	l.wg.Wait()
}

func (l *Lifecycle) Context() context.Context {
	return l.ctx
}
