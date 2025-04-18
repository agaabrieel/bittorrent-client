package session

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"net"
	"os"
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
	Logger           *log.Logger
	Router           *messaging.Router
	ClientId         [20]byte
	Port             int
	Metainfo         *metainfo.TorrentMetainfo
	RecvCh           <-chan messaging.Message
	ErrCh            chan error
	wg               *sync.WaitGroup
	mu               *sync.Mutex
	coreComponentIds []string
}

func NewSessionManager(filepath string, r *messaging.Router) (*SessionManager, error) {

	meta := metainfo.NewMetainfo()
	if err := meta.Deserialize(filepath); err != nil {
		return nil, fmt.Errorf("failed to parse torrent file: %w", err)
	}

	var clientId [20]byte
	rand.Read(clientId[:])

	id, ch := "session_manager", make(chan messaging.Message, 1024)

	logFile, err := os.Create(fmt.Sprintf("%s-%s", id, meta.Infohash))
	if err != nil {
		return nil, fmt.Errorf("failed to create log file: %v", err)
	}
	logger := log.New(logFile, logFile.Name(), 10111)

	err = r.RegisterComponent(id, ch)
	if err != nil {
		return nil, fmt.Errorf("failed to register component with id %s: %v", id, err)
	}

	return &SessionManager{
		id:       id,
		Router:   messaging.NewRouter(logger),
		Logger:   logger,
		ClientId: clientId,
		Metainfo: meta,
		Port:     6081,
		ErrCh:    make(chan error, 1024),
		RecvCh:   ch,
		mu:       &sync.Mutex{},
		wg:       &sync.WaitGroup{},
	}, nil

}

func (mngr *SessionManager) Run(filepath string) {

	TrackerManager, err := tracker.NewTrackerManager(mngr.Metainfo, mngr.Router, mngr.ClientId)
	if err != nil {
		mngr.Logger.Fatalf("failed to create tracker manager: %v", err)
	}

	PeerOrchestrator, err := peer.NewPeerOrchestrator(mngr.Metainfo, mngr.Router, mngr.ClientId)
	if err != nil {
		mngr.Logger.Fatalf("failed to create peer orchestrator: %v", err)
	}

	PieceManager, err := piece.NewPieceManager(mngr.Metainfo, mngr.Router, mngr.ClientId)
	if err != nil {
		mngr.Logger.Fatalf("failed to create piece manager: %v", err)
	}

	IOManager, err := io.NewIOManager(mngr.Metainfo, mngr.Router)
	if err != nil {
		mngr.Logger.Fatalf("failed to create io manager: %v", err)
	}

	ctx, ctxCancel := context.WithCancel(context.Background())

	mngr.wg.Add(1)
	go TrackerManager.Run(ctx, mngr.wg)

	mngr.wg.Add(1)
	go PeerOrchestrator.Run(ctx, mngr.wg)

	mngr.wg.Add(1)
	go PieceManager.Run(ctx, mngr.wg)

	mngr.wg.Add(1)
	go IOManager.Run(ctx, mngr.wg)

	defer mngr.wg.Wait()

	listener, err := net.Listen("tcp", net.JoinHostPort("localhost", strconv.Itoa(mngr.Port)))
	if err != nil {
		panic(err)
	}
	defer listener.Close()

	for {
		select {
		case <-ctx.Done():
			mngr.Logger.Fatalf("context sent: %v", ctx.Err())
			ctxCancel()

		default:
			conn, err := listener.Accept()
			if err != nil {
				mngr.Logger.Printf("connection from %v failed: %v", conn.RemoteAddr(), err)
				conn.Close()
				continue
			}

			mngr.Router.Send("peer_orchestrator", messaging.Message{
				SourceId:    mngr.id,
				PayloadType: messaging.PeerConnected,
				Payload:     messaging.PeerConnectedPayload{Conn: conn},
				CreatedAt:   time.Now(),
			})
		}
	}
}
