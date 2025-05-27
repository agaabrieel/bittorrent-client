package errors

import (
	"context"
	"fmt"
	"time"

	"github.com/agaabrieel/bittorrent-client/pkg/messaging"
)

type ErrorHandler struct {
	RecvCh     <-chan messaging.Message
	FatalErrCh chan<- any
}

func NewErrorHandler(r *messaging.Router, fatalErrCh chan<- any) (*ErrorHandler, error) {

	id, errCh := "error_handler", make(chan messaging.Message, 1024)
	err := r.RegisterComponent(id, errCh)
	if err != nil {
		return nil, fmt.Errorf("failed to register component with id %v: %v", id, err)
	}

	return &ErrorHandler{
		RecvCh:     errCh,
		FatalErrCh: fatalErrCh,
	}, nil
}

func (h *ErrorHandler) Run(ctx context.Context) {

	select {
	case <-ctx.Done():
		fmt.Print("Error handler context is done, exiting")
		return
	case msg := <-h.RecvCh:

		err, ok := msg.Payload.(messaging.ErrorPayload)
		if !ok {
			// stuff
		}

		fmt.Printf("[%+v] %v (id=%v)", err.Severity, err.Message, err.ComponentId)
		if err.Severity == messaging.Critical {
			select {
			case h.FatalErrCh <- true:
				// handle error
			case <-time.After(5 * time.Second):
				fmt.Printf("Lifecycle manager channel is closed, exiting")
				fmt.Printf("Message: %v", err.Message)
				fmt.Printf("ComponentId: %v", err.ComponentId)
				fmt.Printf("Severity: %v", err.Severity)
				fmt.Printf("ErrorCode: %v", err.ErrorCode)
				fmt.Printf("Time: %v", err.Time)
				fmt.Printf("Exiting")
			}
			return
		}

	}
}
