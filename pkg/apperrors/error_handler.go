package apperrors

import (
	"context"
	"log"
	"os"
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
	*log.Logger
	RecvCh            <-chan Error
	ContextCancelFunc context.CancelFunc
}

func NewErrorHandler(ctxCancelFunc context.CancelFunc) (*ErrorHandler, chan<- Error, error) {

	f, err := os.Create("errors.log")
	if err != nil {
		return nil, nil, err
	}

	logger := log.New(f, "", 10111)

	errCh := make(chan Error, 2056)

	return &ErrorHandler{
		RecvCh:            errCh,
		ContextCancelFunc: ctxCancelFunc,
		Logger:            logger,
	}, errCh, nil
}

func (h *ErrorHandler) Run(wg *sync.WaitGroup) {

	defer wg.Done()

	for msg := range h.RecvCh {
		h.Logger.Printf("[%v] %v: %v (id=%v)", msg.Severity, msg.Err.Error(), msg.Message, msg.ComponentId)
		if msg.Severity == Critical {
			h.ContextCancelFunc()
			return
		}
	}
}
