package messaging

import "fmt"

// message topics follow the format source.object.action.destination
// wildcard * for any individual segment

type Actor string
type Object string
type Action string

const (
	PeerManager      Actor = "peer_manager"
	PeerOrchestrator Actor = "peer_orchestrator"
	PieceManager     Actor = "piece_manager"
	IOManager        Actor = "io_manager"
	TrackerManager   Actor = "tracker_manager"
	TorrentManager   Actor = "torrent_manager"
	AnyActor         Actor = "*"
)

const (
	Block     Object = "block"
	Piece     Object = "piece"
	Peer      Object = "peer"
	AnyObject Object = "*"
)

const (
	Request    Action = "request"
	Send       Action = "send"
	Connected  Action = "connected"
	Discovered Action = "discovered"
	AnyAction  Action = "*"
)

func newTopic(source Actor, obj Object, action Action, destination Actor) string {
	return fmt.Sprintf("%s.%s.%s.%s", source, obj, action, destination)
}
