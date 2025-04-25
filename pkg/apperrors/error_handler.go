package apperrors

import (
	"context"
	"sync"
	"time"
)

type ErrorSeverity uint8

const (
	Warning ErrorSeverity = iota
	Critical
)

type Error struct {
	Err         error
	Message     string
	Severity    ErrorSeverity
	Time        time.Time
	ComponentId string
}

type ErrorHandler struct {
	RecvCh <-chan Error
}

func NewErrorHandler() (*ErrorHandler, chan<- Error, error) {

	errCh := make(chan Error, 2056)

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
			println(msg)
			// handle error
		case <-ctx.Done():
			ctxCancel()
		}
	}
}
