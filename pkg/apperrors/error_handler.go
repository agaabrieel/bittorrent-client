package apperrors

import (
	"context"
	"log"
	"os"
	"time"
)

type ErrorSeverity uint8

const (
	Info ErrorSeverity = iota
	Warning
	Critical
)

type ErrorCode uint8

const (
	ErrCodeContextCancelled ErrorCode = iota
	ErrCodeInvalidMessage
	ErrCodeInvalidPayload
	ErrCodeInvalidBlock
	ErrCodeInvalidConnection
	ErrCodeInvalidTracker
	ErrCodeInvalidPiece
	ErrCodeInvalidRequest
	ErrCodeInvalidResponse
	ErrCodeInvalidFile
)

type Error struct {
	Err         error
	Message     string
	Severity    ErrorSeverity
	ErrorCode   ErrorCode
	Time        time.Time
	ComponentId string
}

type ErrorHandler struct {
	*log.Logger
	RecvCh             <-chan Error
	LifecycleManagerCh chan<- bool
}

func NewErrorHandler(lifecycleCh chan<- bool) (*ErrorHandler, chan<- Error, error) {

	f, err := os.Create("errors.log")
	if err != nil {
		return nil, nil, err
	}

	logger := log.New(f, "", 10111)

	errCh := make(chan Error, 2056)

	return &ErrorHandler{
		RecvCh:             errCh,
		LifecycleManagerCh: lifecycleCh,
		Logger:             logger,
	}, errCh, nil
}

func (h *ErrorHandler) Run(ctx context.Context) {

	select {
	case <-ctx.Done():
		h.Logger.Printf("Error handler context is done, exiting")
		return
	case msg := <-h.RecvCh:
		if msg.Err == nil {
			h.Logger.Printf("[%+v] %v: %v (id=%v)", msg.Severity, msg.Err.Error(), msg.Message, msg.ComponentId)
			if msg.Severity == Critical {
				select {
				case h.LifecycleManagerCh <- true:
				case <-time.After(5 * time.Second):
					h.Logger.Printf("Lifecycle manager channel is closed, exiting")
					h.Logger.Printf("Error: %v", msg.Err)
					h.Logger.Printf("Message: %v", msg.Message)
					h.Logger.Printf("ComponentId: %v", msg.ComponentId)
					h.Logger.Printf("Severity: %v", msg.Severity)
					h.Logger.Printf("ErrorCode: %v", msg.ErrorCode)
					h.Logger.Printf("Time: %v", msg.Time)
					h.Logger.Printf("Exiting")
				}
				return
			}
		}
	}
}
