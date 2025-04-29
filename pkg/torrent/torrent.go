package session

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/agaabrieel/bittorrent-client/pkg/apperrors"
	"github.com/agaabrieel/bittorrent-client/pkg/io"
	"github.com/agaabrieel/bittorrent-client/pkg/lifecycle"
	logger "github.com/agaabrieel/bittorrent-client/pkg/log"
	messaging "github.com/agaabrieel/bittorrent-client/pkg/messaging"
	metainfo "github.com/agaabrieel/bittorrent-client/pkg/metainfo"
	peer "github.com/agaabrieel/bittorrent-client/pkg/peers"
	piece "github.com/agaabrieel/bittorrent-client/pkg/piece"
	tracker "github.com/agaabrieel/bittorrent-client/pkg/tracker"
)

const CLIENT_PORT = 6881

type SessionManager struct {
	id       string
	Router   *messaging.Router
	ClientId [20]byte
	Port     int
	Metainfo *metainfo.TorrentMetainfo
	RecvCh   <-chan messaging.Message
	ErrCh    chan error
	wg       *sync.WaitGroup
	mu       *sync.Mutex
}

func NewSessionManager(filepath string, r *messaging.Router) (*SessionManager, error) {

	meta := metainfo.NewMetainfo()
	if err := meta.Deserialize(filepath); err != nil {
		return nil, fmt.Errorf("failed to parse torrent file: %w", err)
	}

	var clientId [20]byte
	rand.Read(clientId[:])

	id, ch := "session_manager", make(chan messaging.Message, 1024)

	err := r.RegisterComponent(id, ch)
	if err != nil {
		return nil, fmt.Errorf("failed to register component with id %s: %v", id, err)
	}

	return &SessionManager{
		id:       id,
		Router:   messaging.NewRouter(),
		ClientId: clientId,
		Metainfo: meta,
		Port:     CLIENT_PORT,
		RecvCh:   ch,
		mu:       &sync.Mutex{},
		wg:       &sync.WaitGroup{},
	}, nil

}

func (mngr *SessionManager) Run() {

	LifecycleManager := lifecycle.NewLifecycle(context.Background())

	ErrorHandler, errCh, err := apperrors.NewErrorHandler(ctxCancel)
	if err != nil {
		panic(err)
	}

	Logger, err := logger.NewLogger(mngr.Metainfo, mngr.Router, errCh, mngr.ClientId)
	if err != nil {
		panic(err)
	}

	TrackerManager, err := tracker.NewTrackerManager(mngr.Metainfo, mngr.Router, errCh, mngr.ClientId)
	if err != nil {
		Logger.Panicf("failed to create tracker manager: %v", err)
	}

	PeerOrchestrator, err := peer.NewPeerOrchestrator(mngr.Metainfo, mngr.Router, errCh, mngr.ClientId)
	if err != nil {
		Logger.Panicf("failed to create peer orchestrator: %v", err)
	}

	PieceManager, err := piece.NewPieceManager(mngr.Metainfo, mngr.Router, errCh, mngr.ClientId)
	if err != nil {
		Logger.Panicf("failed to create piece manager: %v", err)
	}

	IOManager, err := io.NewIOManager(mngr.Metainfo, mngr.Router, errCh)
	if err != nil {
		Logger.Panicf("failed to create io manager: %v", err)
	}

	mngr.wg.Add(1)
	go ErrorHandler.Run(mngr.wg)

	mngr.wg.Add(1)
	go Logger.Run(ctx, mngr.wg)

	mngr.wg.Add(1)
	go PeerOrchestrator.Run(ctx, mngr.wg)

	mngr.wg.Add(1)
	go PieceManager.Run(ctx, mngr.wg)

	mngr.wg.Add(1)
	go IOManager.Run(ctx, mngr.wg)

	mngr.wg.Add(1)
	go TrackerManager.Run(ctx, mngr.wg)

	defer mngr.wg.Wait()

	listener, err := net.Listen("tcp", net.JoinHostPort("localhost", strconv.Itoa(mngr.Port)))
	if err != nil {
		Logger.Panicf("failed to create io manager: %v", err)
	}
	defer listener.Close()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				errCh <- apperrors.Error{
					Err:         err,
					Message:     "listener failed (timeout, closed, etc)",
					Severity:    apperrors.Critical,
					ErrorCode:   apperrors.ErrCodeInvalidConnection,
					ComponentId: "session_manager",
					Time:        time.Now(),
				}
				return
			}

			mngr.Router.Send("peer_orchestrator", messaging.Message{
				SourceId:    mngr.id,
				ReplyTo:     mngr.id,
				PayloadType: messaging.PeerConnected,
				Payload:     messaging.PeerConnectedPayload{Conn: conn},
				CreatedAt:   time.Now(),
			})
		}
	}()
	<-ctx.Done()
}
