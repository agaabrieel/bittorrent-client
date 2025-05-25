package messaging

import (
	"fmt"
	"os"
	"sync"
	"time"
)

type Router struct {
	Registry      map[string]chan<- Message
	MessageBuffer map[Message]string
	mu            sync.RWMutex
}

func NewRouter() *Router {
	return &Router{
		Registry:      make(map[string]chan<- Message, 1024),
		MessageBuffer: make(map[Message]string, 1024),
		mu:            sync.RWMutex{},
	}
}

func (r *Router) RegisterComponent(id string, ch chan Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.Registry[id]; !ok {
		r.Registry[id] = ch
		return nil
	}
	return fmt.Errorf("component id %v already registered", id)
}

func (r *Router) Send(destId string, msg Message) {

	r.mu.Lock()
	defer r.mu.Unlock()

	r.log(msg)

	if ch, ok := r.Registry[destId]; ok {
		select {
		case ch <- msg:
		default:
			r.MessageBuffer[msg] = destId
			r.log(Message{
				SourceId:    "router",
				ReplyTo:     "router",
				PayloadType: Error,
				Payload: ErrorPayload{
					Msg: "Channel is blocked. Adding message to message buffer for retries.",
				},
				CreatedAt: time.Now(),
			})
		}
	} else {
		r.log(Message{
			SourceId:    "router",
			ReplyTo:     "router",
			PayloadType: Error,
			Payload: ErrorPayload{
				Msg: "Invalid destination Id. Dropping message.",
			},
			CreatedAt: time.Now(),
		})
	}
}

func (r *Router) Broadcast(msg Message) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, ch := range r.Registry {
		select {
		case ch <- msg:
		default:
			r.MessageBuffer[msg] = "*"
			continue
		}
	}
}

func (r *Router) FlushMessageBuffer() {

	r.mu.RLock()
	defer r.mu.RUnlock()

	deliveredMessages := make([]Message, 1024)

	for msg, id := range r.MessageBuffer {

		if id == "*" {
			r.Broadcast(msg)
			continue
		}

		select {
		case r.Registry[id] <- msg:
			deliveredMessages = append(deliveredMessages, msg)
		default:
			r.log(Message{
				SourceId:    "router",
				ReplyTo:     "router",
				PayloadType: Error,
				Payload: ErrorPayload{
					Msg: "Channel is blocked. Keeping message in message buffer for retries.",
				},
				CreatedAt: time.Now(),
			})
		}
	}

	for _, msg := range deliveredMessages {
		delete(r.MessageBuffer, msg)
	}
}

func (r *Router) log(msg Message) {
	if logger, ok := r.Registry["logger"]; ok {
		select {
		case logger <- msg:
		default:
			os.Stderr.WriteString("Failed to send log message to logger")
		}
	}
}

// type Middleware func(msg Message, str string)

// func chainMiddlewares(middlewares ...Middleware) Middleware {
// 	return func(msg Message, str string) Middleware {
// 		var err error
// 		for _, middleware := range middlewares {
// 			_, err = middleware(msg, str)
// 		}
// 	}
// }

// func (r *Router) RegisterMiddleware(f Middleware) {
// 	r.mu.Lock()
// 	defer r.mu.Unlock()
// 	r.Middleware = append(r.Middleware, f)
// }

// func logger(next Middleware) Middleware {
// 	return func(msg Message, str string) {
// 		log.Default().Printf("message from component %s to component %s", msg.SourceId, str)
// 		next(msg, str)
// 		log.Default().Printf("message received")
// 	}
// }
