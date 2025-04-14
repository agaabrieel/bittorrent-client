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

type MessageType uint8

const (
	BlockRequest MessageType = iota
	BlockSend
	PieceSend
	PieceRequest
	NewPeerFromTracker
	NewPeerConnection
	PeerUpdate
	AnnounceDataRequest
	AnnounceDataResponse
	TrackerData
	PieceValidated
	PieceInvalidated
	FileFinished
	Error
)

type Message struct {
	MessageType MessageType
	Data        any
}

type PeerManagerMessage struct {
	Message Message
	Id      [20]byte
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
	Subscribers map[MessageType][]chan<- Message
	Mutex       *sync.RWMutex
}

func (r *Router) Subscribe(msgType MessageType, ch chan<- Message) {
	r.Subscribers[msgType] = append(r.Subscribers[msgType], ch)
}

func (r *Router) Start(commCh chan Message, ctx context.Context) {

	select {
	case <-ctx.Done():
		return
	default:
		for msg := range commCh {
			for _, ch := range r.Subscribers[msg.MessageType] {
				ch <- msg
			}
		}
	}
}
