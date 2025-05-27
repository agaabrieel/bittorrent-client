package messaging

import (
	"net"
	"time"

	"github.com/bits-and-blooms/bitset"
)

type MessageType uint8

const (
	BlockRequest MessageType = iota
	BlockSend
	PieceRequest
	PieceSend
	NextPieceIndexRequest
	NextPieceIndexSend
	NextBlockIndexRequest
	NextBlockIndexSend
	AnnounceDataRequest
	AnnounceDataSend
	PeersDiscovered
	PeerConnected
	PeerBitfield
	PeerBitfieldUpdate
	PieceValidated
	PieceInvalidated
	Acknowledged
	Error
)

type Message struct {
	MsgId       string
	SourceId    string
	ReplyTo     string
	ReplyingTo  string
	PayloadType MessageType
	Payload     any
	CreatedAt   time.Time
}

type BroadcastedMessage struct {
	Message
	Topic string
}

type BlockRequestPayload struct {
	Index  uint32
	Offset uint32
	Size   uint32
}

type BlockSendPayload struct {
	Index  uint32
	Offset uint32
	Size   uint32
	Data   []byte
}

type PieceRequestPayload struct {
	Index uint32
	Size  uint32
}

type PieceSendPayload struct {
	Index uint32
	Size  uint32
	Data  []byte
}

type AnnounceDataRequestPayload struct {
}

type AnnounceDataSendPayload struct {
	Uploaded   uint64
	Downloaded uint64
	Left       uint64
	Event      string
}

type PeersDiscoveredPayload struct {
	Addrs []net.Addr
}

type PeerConnectedPayload struct {
	Conn net.Conn
}

type PieceValidatedPayload struct {
	Index uint32
}

type PieceInvalidatedPayload struct {
	Index uint32
}

type PeerBitfieldPayload struct {
	Bitfield *bitset.BitSet
}

type PeerBitfieldUpdatePayload struct {
	Index int
}

type NextPieceIndexRequestPayload struct {
}

type NextPieceIndexSendPayload struct {
	Index int
}

type NextBlockIndexRequestPayload struct {
	PieceIndex int
}

type NextBlockIndexSendPayload struct {
	PieceIndex int
	Offset     int
	Size       int
}
