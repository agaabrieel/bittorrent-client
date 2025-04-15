package messaging

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
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

// interesting approach, but possibly over engineered for <10 components in the architecture
// format source:object:action:destination
type Topic string

const (
	PeerMngr_Block_Request_PieceMngr     Topic = "peer_mngr:block:request:piece_mngr"
	PeerMngr_Block_Send_PieceMngr        Topic = "peer_mngr:block:send:piece_mngr"
	PieceMngr_Piece_Send_IOMngr          Topic = "piece_mngr:piece:send:io_mngr"
	PieceMngr_Piece_Request_IOMngr       Topic = "piece_mngr:piece:request:io_mngr"
	PieceMngr_Block_Request_IOMngr       Topic = "piece_mngr:block:request:io_mngr"
	TrackerMngr_Peer_Discovered_PeerOrch Topic = "tracker_mngr:peer:discovered:peer_orch"
	TorrentMngr_Peer_Connected_PeerOrch  Topic = "torrent_mngr:peer:connected:peer_orch"
)

// ------------------------------------------------------------------------------------------//
type ComponentID uint8

const (
	PeerManager ComponentID = iota
	PeerOrchestrator
	PieceManager
	IOManager
	TrackerManager
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
	Type        MessageType
	Payload     any
	Source      ComponentID
	Destination ComponentID
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
	Shards map[ComponentID]*Shard
}

type Shard struct {
	Subscribers map[ComponentID]chan Message
	Mutex       *sync.RWMutex
	commCh      chan Message
}

func (r *Router) Subscribe(srcId, destId ComponentID) {
	r.Shards[id].Subscribers[id]
}

func (r *Router) Publish() {

}

func (r *Router) Run(globalCh chan Message, ctx context.Context) {

	r.Mutex.RLock()
	defer r.Mutex.RUnlock()

	select {
	case <-ctx.Done():
		return
	case msg := <-globalCh:
		if r.Subscribers[msg.Destination] != nil {
			r.Subscribers[msg.Destination] <- msg
		}
	default:
		// log, etc
	}
}
