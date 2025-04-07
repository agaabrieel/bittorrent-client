package messaging

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
	PeerSend
	PeerUpdate
	AnnounceDataRequest
	AnnounceDataResponse
	PieceValidated
	PieceInvalidated
	FileFinished
)

type Message struct {
	MessageType MessageType
	Data        any
}

type Router struct {
	Subscribers map[MessageType][]chan<- Message
}

func (r *Router) Subscribe(msgType MessageType, ch chan<- Message) {
	r.Subscribers[msgType] = append(r.Subscribers[msgType], ch)
}

func (r *Router) Start(commCh chan Message) {
	for msg := range commCh {
		for _, ch := range r.Subscribers[msg.MessageType] {
			ch <- msg
		}
	}
}
