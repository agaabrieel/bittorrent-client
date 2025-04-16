package messaging

import (
	"fmt"
	"strings"
)

// interesting idea but possibly overkill
// message topics follow the format source.object.action.destination
// wildcard * for any individual segment

type Topic string

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
)

const (
	Block Object = "block"
	Piece Object = "piece"
	Peer  Object = "peer"
)

const (
	Request    Action = "request"
	Send       Action = "send"
	Connected  Action = "connected"
	Discovered Action = "discovered"
)

func generateAllPatterns() []string {

	actors := []Actor{PeerManager, PeerOrchestrator, PieceManager, IOManager, TrackerManager, TorrentManager}
	objects := []Object{Block, Piece, Peer}
	actions := []Action{Request, Send, Connected, Discovered}

	topicsLen := max(len(actors), len(objects), len(actions))
	patterns := make([]string, topicsLen)

	for _, src := range actors {
		for _, obj := range objects {
			for _, action := range actions {
				for _, dest := range actors {
					patterns = append(patterns, newTopic(src, obj, action, dest))
				}
			}
		}
	}
	return patterns
}

func newTopic(source Actor, obj Object, action Action, destination Actor) string {
	return fmt.Sprintf("%s.%s.%s.%s", string(source), string(obj), string(action), string(destination))
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
		}
		return false
	}
	return true
}

// func (r *Router) Subscribe(id, pattern string) {
// 	r.mu.Lock()
// 	defer r.mu.Unlock()
// }

// func (r *Router) Publish(msg Message, topic string) {
// 	r.mu.RLock()
// 	defer r.mu.RUnlock()
// 	for pattern, channels := range r.Subscribers {
// 		if matchTopic(pattern, topic) {
// 			for _, ch := range channels {
// 				select {
// 				case ch <- msg:
// 				default:
// 					// drop the message
// 					// log some shiet
// 					// idk, do something
// 				}
// 			}
// 		}
// 	}
// }
