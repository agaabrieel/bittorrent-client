package messaging

import "time"

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
	SourceId    string
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

type PieceSendPayload struct {
	Index uint32
	Size  uint32
	Data  []byte
}

type PieceRequestPayload struct {
	Index uint32
	Size  uint32
}

type AnnounceDataRequestPayload struct {
}

type AnnounceDataSendPayload struct {
	Uploaded   uint64
	Downloaded uint64
	Left       uint64
	Event      string
}
