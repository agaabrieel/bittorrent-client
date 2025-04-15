package messaging

import (
	"strings"
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
	ID          uuid.UUID
	PayloadType MessageType
	Topic       string
	Payload     any
	ReplyCh     chan<- Message
	CreatedAt   time.Time
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

type Router struct {
	Subscribers map[string][]chan<- Message
	mu          *sync.RWMutex
}

func (r *Router) Subscribe(topic string, ch chan<- Message) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Subscribers[topic] = append(r.Subscribers[topic], ch)
}

func (r *Router) Publish(msg Message) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for topic, chans := range r.Subscribers {
		if matchTopic(topic, msg.Topic) {
			for _, ch := range chans {
				ch <- msg
			}
		}
	}
}

func matchTopic(pattern, topic string) bool {
	pattern_segments := strings.Split(pattern, ".")
	topic_segments := strings.Split(topic, ".")

	if len(pattern_segments) != len(topic_segments) {
		return false
	}

	for idx, pattern_segment := range pattern_segments {
		if pattern_segment == "*" || pattern_segment == topic_segments[idx] {
			continue // continues to next segment if segments match or if pattern contains a wildcard
		} else {
			return false
		}
	}
	return true
}
