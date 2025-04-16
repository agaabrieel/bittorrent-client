package messaging

import (
	"log"
	"sync"

	"github.com/google/uuid"
)

type Middleware func(msg Message, str string) Middleware

type Router struct {
	Registry    map[string]chan<- Message
	Subscribers map[string][]chan<- Message
	Middleware  []Middleware
	mu          *sync.RWMutex
}

func NewRouter() *Router {
	return &Router{
		Registry:    make(map[string]chan<- Message, 1024),
		Subscribers: make(map[string][]chan<- Message, 1024),
		Middleware:  make([]Middleware, 10),
		mu:          &sync.RWMutex{},
	}
}

func chainMiddlewares(middlewares ...Middleware) Middleware {
	return func(msg Message, str string) Middleware {
		var err error
		for _, middleware := range middlewares {
			_, err = middleware(msg, str)
		}
	}
}

func (r *Router) RegisterMiddleware(f Middleware) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Middleware = append(r.Middleware, f)
}

func (r *Router) NewComponent() (string, chan Message) {
	id := uuid.New().String()
	ch := make(chan Message, 256)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Registry[id] = ch
	return id, ch
}

func (r *Router) Send(msg Message, destId string) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for i := len(r.Middleware) - 1; i <= 0; i-- {

	}

	if ch, ok := r.Registry[destId]; ok {
		ch <- msg
	}
}

func (r *Router) Publish(msg Message, topic string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for pattern, channels := range r.Subscribers {
		if matchTopic(pattern, topic) {
			for _, ch := range channels {
				select {
				case ch <- msg:
				default:
					// drop the message
					// log some shiet
					// idk, do something
				}
			}
		}
	}
}

func loggerExampleMiddleware(next Middleware) func(msg Message, str string) {
	return func(msg Message, str string) {
		log.Default().Print("test")
		next()
		log.Default().Print("end")
	}
}
