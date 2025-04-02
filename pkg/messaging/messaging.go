package messaging

type MessageType uint8

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

const (
	NilMsg MessageType = iota
	PieceValidated
	PieceInvalidated
	FileFinished
	BlockRequest
	BlockSend
)

type PeerToPieceManagerMsg struct {
	MessageType MessageType
	Data        []byte // STRUCTURE DEFINED BY THE ABOVE MESSAGE PROTOCOL
	ReplyCh     chan<- PieceManagerToPeerMsg
}

type PieceManagerToPeerMsg struct {
	MessageType MessageType
	Data        []byte
}
