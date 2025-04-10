package session

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"net"
	"strconv"
	"sync"

	messaging "github.com/agaabrieel/bittorrent-client/pkg/messaging"
	metainfo "github.com/agaabrieel/bittorrent-client/pkg/metainfo"
	peer "github.com/agaabrieel/bittorrent-client/pkg/peers"
	piece "github.com/agaabrieel/bittorrent-client/pkg/piece"
	tracker "github.com/agaabrieel/bittorrent-client/pkg/tracker"
)

type SessionManager struct {
	Id        [20]byte
	Port      int
	Metainfo  metainfo.TorrentMetainfo
	RecvCh    <-chan messaging.Message
	SendCh    chan<- messaging.Message
	WaitGroup sync.WaitGroup
	Mutex     sync.Mutex
}

func NewTorrentSession(filepath string) (*SessionManager, error) {

	meta := metainfo.NewMetainfo()
	if err := meta.Deserialize(filepath); err != nil {
		return nil, fmt.Errorf("failed to parse torrent file: %w", err)
	}

	var id [20]byte
	rand.Read(id[:])

	return &SessionManager{
		Id:        id,
		Port:      6081,
		SendCh:    make(chan messaging.Message, 256),
		RecvCh:    make(chan messaging.Message, 256),
		Mutex:     sync.Mutex{},
		WaitGroup: sync.WaitGroup{},
	}, nil

}

func (t *SessionManager) Run() {

	Router := messaging.Router{
		Subscribers: make(map[messaging.MessageType][]chan<- messaging.Message),
	}

	globalCh := make(chan messaging.Message)

	TrackerManager := tracker.NewTrackerManager(t.Metainfo, &Router, globalCh, t.Id)

	PeerManager := peer.NewPeerOrchestrator(t.Metainfo, &Router, globalCh, t.Id)

	PieceManager := piece.NewPieceManager(t.Metainfo, &Router, globalCh)

	ctx, ctxCancel := context.WithCancel(context.Background())

	t.WaitGroup.Add(4)
	defer t.WaitGroup.Wait()

	go Router.Start(globalCh)
	go TrackerManager.Run(ctx, &t.WaitGroup)
	go PeerManager.Run(ctx, &t.WaitGroup)
	go PieceManager.Run(ctx, &t.WaitGroup)

	listener, err := net.Listen("tcp", net.JoinHostPort("localhost", strconv.Itoa(t.Port)))
	if err != nil {
		panic(err)
	}
	defer listener.Close()

	t.WaitGroup.Add(1)
	go func() {
		defer t.WaitGroup.Done()
		var conn net.Conn
		for {
			select {
			case <-ctx.Done():
				log.Default().Printf("context sent: %v", ctx.Err())
				return

			default:
				conn, err = listener.Accept()
				if err != nil {
					log.Default().Printf("connection from %v rejected: %v", conn.RemoteAddr(), err)
					conn.Close()
					continue
				}

				t.SendCh <- messaging.Message{
					MessageType: messaging.NewPeerConnection,
					Data: peer.PeerManager{
						PeerConn:  conn,
						Mutex:     &sync.Mutex{},
						WaitGroup: &sync.WaitGroup{},
					},
				}
			}
		}
	}()

	t.WaitGroup.Add(1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				log.Default().Printf("context sent: %v", ctx.Err())
				return
			case msg := <-t.RecvCh:
				switch msg.MessageType {
				case messaging.FileFinished:
					ctxCancel()
				}
			}
		}
	}()

}
