package messaging

import (
	"context"
	"sync"
)

// NilMsg
// Data = nil

// PieceValidated
// Data = uint32 piece index

// PieceInvalidated
// Data = uint32 piece index

// FileFinished
// Data = ?

// BlockRequest
// Data = uint32 piece index, uint32 block offset, uint32 block length

// BlockSend
// Data = uint32 piece index, uint32 block offset, uint32 block length

type MessageActor uint8
type MessageObject uint8
type MessageAction uint8

const (
	PeerManager MessageActor = iota
	PieceManager
	IOManager
	PeerOrchestrator
	TrackerManager
)

const (
	Piece MessageObject = iota
	Block
	Peer
)

const (
	Request MessageAction = iota
	Send
)

type Message struct {
	Source      MessageActor
	Destination MessageActor
	Object      MessageObject
	Action      MessageAction
	Data        any
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
	Subscribers map[MessageActor]map[MessageObject]map[MessageAction]map[MessageActor]chan<- Message
	Mutex       *sync.RWMutex
}

func (r *Router) Subscribe(source MessageActor, obj MessageObject, action MessageAction, destination MessageActor, ch chan<- Message) {
	r.Subscribers[source][obj][action][destination] = ch
}

func (r *Router) Run(commCh chan Message, ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	default:
		for msg := range commCh {
			r.Subscribers[msg.Source][msg.Object][msg.Action][msg.Destination] <- msg
		}
	}
}
