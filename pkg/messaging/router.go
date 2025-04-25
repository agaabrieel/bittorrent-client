package messaging

import (
	"fmt"
	"sync"
)

type Router struct {
	Registry map[string]chan<- Message
	mu       *sync.RWMutex
}

func NewRouter() *Router {
	return &Router{
		Registry: make(map[string]chan<- Message, 1024),
		mu:       &sync.RWMutex{},
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

	r.mu.RLock()
	defer r.mu.RUnlock()

	r.Registry["logger"] <- msg

	if ch, ok := r.Registry[destId]; ok {
		select {
		case ch <- msg:
		default:
			//log
		}
	}
}

func (r *Router) Broadcast(msg Message) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, ch := range r.Registry {
		select {
		case ch <- msg:
		default:
			//log
			continue
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
