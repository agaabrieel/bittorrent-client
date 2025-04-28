package peer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/agaabrieel/bittorrent-client/pkg/apperrors"
	messaging "github.com/agaabrieel/bittorrent-client/pkg/messaging"
	metainfo "github.com/agaabrieel/bittorrent-client/pkg/metainfo"
	"github.com/bits-and-blooms/bitset"
)

type PeerOrchestrator struct {
	id             string
	Router         *messaging.Router
	clientId       [20]byte
	clientInfohash [20]byte
	Bitfield       *bitset.BitSet
	Mutex          *sync.RWMutex
	Waitgroup      *sync.WaitGroup
	RecvCh         <-chan messaging.Message
	ErrorCh        chan<- apperrors.Error
}

func NewPeerOrchestrator(meta *metainfo.TorrentMetainfo, r *messaging.Router, errCh chan<- apperrors.Error, clientId [20]byte) (*PeerOrchestrator, error) {

	id, ch := "peer_orchestrator", make(chan messaging.Message, 1024)
	err := r.RegisterComponent(id, ch)
	if err != nil {
		return nil, fmt.Errorf("failed to register component with id %s: %v", id, err)
	}

	bsSize := uint(float64((meta.InfoDict.Length / meta.InfoDict.PieceLength) / 8))

	return &PeerOrchestrator{
		id:             id,
		clientId:       clientId,
		clientInfohash: meta.Infohash,
		Bitfield:       bitset.New(bsSize),
		RecvCh:         ch,
		ErrorCh:        errCh,
		Mutex:          &sync.RWMutex{},
		Waitgroup:      &sync.WaitGroup{},
	}, nil
}

func (mngr *PeerOrchestrator) Run(ctx context.Context, wg *sync.WaitGroup) {

	childCtx, ctxCancel := context.WithCancel(ctx)
	defer ctxCancel()
	defer wg.Done()

	for {
		select {
		case msg := <-mngr.RecvCh:
			switch msg.PayloadType {
			case messaging.PeersDiscovered:

				payload, ok := msg.Payload.(messaging.PeersDiscoveredPayload)
				if !ok {
					mngr.ErrorCh <- apperrors.Error{
						Err:         errors.New("incorrect payload"),
						Message:     fmt.Sprintf("incorrect payload: %v", msg),
						Severity:    apperrors.Warning,
						ErrorCode:   apperrors.ErrCodeInvalidPayload,
						Time:        time.Now(),
						ComponentId: mngr.id,
					}
					continue
				}

				wg.Add(1)
				go func() {
					defer wg.Done()
					for _, addr := range payload.Addrs {
						peerMngr := NewPeerManager(mngr.Router, nil, addr, mngr.ErrorCh)
						wg.Add(1)
						defer wg.Done()
						go peerMngr.startPeerHandshake(childCtx, mngr.clientInfohash, mngr.clientId)
					}
				}()

			case messaging.PeerConnected:
				payload, ok := msg.Payload.(messaging.PeerConnectedPayload)
				if !ok {
					mngr.ErrorCh <- apperrors.Error{
						Err:         errors.New("incorrect payload"),
						Message:     fmt.Sprintf("incorrect payload: %v", msg),
						Severity:    apperrors.Warning,
						Time:        time.Now(),
						ComponentId: mngr.id,
					}
					continue
				}

				wg.Add(1)
				go func() {
					defer wg.Done()
					peerMngr := NewPeerManager(mngr.Router, payload.Conn, payload.Conn.RemoteAddr(), mngr.ErrorCh)
					go peerMngr.replyToPeerHandshake(childCtx, mngr.clientInfohash, mngr.clientId)
				}()

			case messaging.PieceValidated:
				payload, ok := msg.Payload.(messaging.PieceValidatedPayload)
				if !ok {
					mngr.ErrorCh <- apperrors.Error{
						Err:         errors.New("incorrect payload"),
						Message:     "incorrect payload",
						Severity:    apperrors.Warning,
						Time:        time.Now(),
						ComponentId: mngr.id,
					}
					continue
				}

				wg.Add(1)
				go func() {
					mngr.Mutex.Lock()
					defer mngr.Mutex.Unlock()
					defer wg.Done()
					mngr.Bitfield.Set(uint(payload.Index))
				}()

			}

		case <-ctx.Done():
			return // should add logging here
		}
	}
}
