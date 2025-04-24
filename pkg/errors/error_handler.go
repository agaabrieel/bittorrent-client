package errors

import (
	"context"
	"sync"
)

type ErrorHandler struct {
	RecvCh <-chan error
}

func NewErrorHandler() (*ErrorHandler, chan<- error, error) {

	errCh := make(chan error, 2056)

	return &ErrorHandler{
		RecvCh: errCh,
	}, errCh, nil
}

func (h *ErrorHandler) Run(ctx context.Context, wg *sync.WaitGroup) {

	defer wg.Done()
	_, ctxCancel := context.WithCancel(ctx)

	for {
		select {
		case msg := <-h.RecvCh:
			msg.Error()
			// handle error
		case <-ctx.Done():
			ctxCancel()
		}
	}
}
