package messaging

import (
	"net"
	"time"
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
	Error
)

type Message struct {
	SourceId    string
	ReplyTo     string
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

type PeerDiscoveredPayload struct {
	Addr net.Addr
}

type PeerConnectedPayload struct {
	Conn net.Conn
}

type PieceValidatedPayload struct {
	Index uint32
}

type ErrorPayload struct {
	Msg string
}
