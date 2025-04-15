package messaging

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

type MessageType uint8

const (
	BlockRequest MessageType = iota
	BlockSend
	PieceRequest
	PieceSend
	PeerDiscovered
	PeerConnected
	PieceValidated
	PieceInvalidated
)

type Message struct {
	ID             uuid.UUID
	SourceID       uuid.UUID
	DestinationIDs []uuid.UUID
	PayloadType    MessageType
	Topic          string
	Payload        any
	CreatedAt      time.Time
}

type Router struct {
	Registry   map[uuid.UUID]chan<- Message
	Middleware []func()
	mu         *sync.RWMutex
}

func NewRouter() *Router {
	return &Router{}
}

func (r *Router) RegisterMiddleware(f func()) {
	r.Middleware = append(r.Middleware, f)
}

func (r *Router) Subscribe(id uuid.UUID, ch chan<- Message) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Registry[id] = ch
}

func (r *Router) Publish(msg Message) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, id := range msg.DestinationIDs {
		r.Registry[id] <- msg
	}
}

type BlockRequestData struct {
	Index  uint32
	Offset uint32
	Size   uint32
}

type BlockSendData struct {
	Index  uint32
	Offset uint32
	Size   uint32
	Data   []byte
}

type PieceSendData struct {
	Index uint32
	Size  uint32
	Data  []byte
}

type PieceRequestData struct {
	Index uint32
	Size  uint32
}

type AnnounceDataRequestData struct {
}

type AnnounceDataResponseData struct {
	Uploaded   uint64
	Downloaded uint64
	Left       uint64
	Event      string
}
