package messaging

import (
	"sync"

	"github.com/google/uuid"
)

type Router struct {
	Registry    map[string]chan<- Message
	Subscribers map[string][]chan<- Message
	Middleware  []func()
	mu          *sync.RWMutex
}

func NewRouter() *Router {
	return &Router{
		Registry:    make(map[string]chan<- Message, 1024),
		Subscribers: make(map[string][]chan<- Message, 1024),
		Middleware:  make([]func(), 10),
		mu:          &sync.RWMutex{},
	}
}

func (r *Router) RegisterMiddleware(f func()) {
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
	if ch, ok := r.Registry[destId]; ok {
		ch <- msg
	}
}

func (r *Router) Publish(msg BroadcastedMessage) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for topic, channels := range r.Subscribers {
		if matchTopic(topic, msg.Topic) {
			for _, ch := range channels {
				select {
				case ch <- msg.Message:
				default:
					// drop the message
					// log some shiet
					// idk, do something
				}
			}
		}
	}
}
