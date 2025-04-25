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
	RecvCh            <-chan Error
	ContextCancelFunc context.CancelFunc
}

func NewErrorHandler(ctxCancelFunc context.CancelFunc) (*ErrorHandler, chan<- Error, error) {

	errCh := make(chan Error, 2056)

	return &ErrorHandler{
		RecvCh:            errCh,
		ContextCancelFunc: ctxCancelFunc,
	}, errCh, nil
}

func (h *ErrorHandler) Run(wg *sync.WaitGroup) {

	defer wg.Done()

	for msg := range h.RecvCh {
		if msg.Severity == Critical {
			h.ContextCancelFunc()
			return
		}
	}
}
