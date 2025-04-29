package lifecycle

import (
	"context"
	"sync"
)

// possibly overkill for lifecycle management lol

type TaskSpawner func(func(ctx context.Context))

type Lifecycle struct {
	wg         sync.WaitGroup
	ctx        context.Context
	cancel     context.CancelFunc
	fatalErrCh <-chan bool
}

func NewLifecycle(parent context.Context) (*Lifecycle, chan<- bool) {
	ctx, cancel := context.WithCancel(parent)
	ch := make(chan bool, 1)
	return &Lifecycle{
		ctx:        ctx,
		cancel:     cancel,
		fatalErrCh: ch,
	}, ch
}

func (l *Lifecycle) Spawner() TaskSpawner {
	return func(fn func(ctx context.Context)) {
		l.Go(fn)
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
