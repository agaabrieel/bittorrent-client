package messaging

import (
	"fmt"
	"strings"
)

// interesting idea but possibly overkill
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
