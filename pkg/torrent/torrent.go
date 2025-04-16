package session

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/agaabrieel/bittorrent-client/pkg/io"
	messaging "github.com/agaabrieel/bittorrent-client/pkg/messaging"
	metainfo "github.com/agaabrieel/bittorrent-client/pkg/metainfo"
	peer "github.com/agaabrieel/bittorrent-client/pkg/peers"
	piece "github.com/agaabrieel/bittorrent-client/pkg/piece"
	tracker "github.com/agaabrieel/bittorrent-client/pkg/tracker"
)

type SessionManager struct {
	id               string
	Router           *messaging.Router
	ClientId         [20]byte
	Port             int
	Metainfo         metainfo.TorrentMetainfo
	RecvCh           <-chan messaging.DirectedMessage
	wg               *sync.WaitGroup
	mu               *sync.Mutex
	coreComponentIds []string
}

func NewTorrentSessionManager(filepath string, r *messaging.Router) (*SessionManager, error) {

	meta := metainfo.NewMetainfo()
	if err := meta.Deserialize(filepath); err != nil {
		return nil, fmt.Errorf("failed to parse torrent file: %w", err)
	}

	var clientId [20]byte
	rand.Read(clientId[:])

	id, ch := r.NewComponent()

	return &SessionManager{
		id:       id,
		Router:   messaging.NewRouter(),
		ClientId: clientId,
		Port:     6081,
		RecvCh:   ch,
		mu:       &sync.Mutex{},
		wg:       &sync.WaitGroup{},
	}, nil

}

func (mngr *SessionManager) Run() {

	TrackerManager := tracker.NewTrackerManager(mngr.Metainfo, mngr.Router, mngr.ClientId)
	PeerOrchestrator := peer.NewPeerOrchestrator(mngr.Metainfo, mngr.Router, mngr.ClientId)
	PieceManager := piece.NewPieceManager(mngr.Metainfo, mngr.Router, mngr.ClientId)
	IOManager := io.NewIOManager(mngr.Metainfo, mngr.Router)

	mngr.coreComponentIds = make([]string, 5)
	mngr.coreComponentIds = append(mngr.coreComponentIds, TrackerManager)

	ctx, ctxCancel := context.WithCancel(context.Background())

	mngr.wg.Add(1)
	go TrackerManager.Run(ctx, mngr.wg)

	mngr.wg.Add(1)
	go PeerOrchestrator.Run(ctx, mngr.wg)

	mngr.wg.Add(1)
	go PieceManager.Run(ctx, mngr.wg)

	defer mngr.wg.Wait()

	listener, err := net.Listen("tcp", net.JoinHostPort("localhost", strconv.Itoa(mngr.Port)))
	if err != nil {
		panic(err)
	}
	defer listener.Close()

	mngr.wg.Add(1)
	go func() {
		defer mngr.wg.Done()
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

				peerMngr := peer.NewPeerManager(mngr.Router, conn)

				mngr.Router.Publish(messaging.DirectedMessage{
					ReplyTo:       mngr.id,
					DestinationID: mngr.id,
					PayloadType:   messaging.PeerConnected,
					Payload:       peerMngr,
					CreatedAt:     time.Now(),
				})

			}
		}
	}()
	ctxCancel()
}
