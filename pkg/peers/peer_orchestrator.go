package peer

import (
	"context"
	"errors"
	"fmt"
	"sync"

	bitfield "github.com/agaabrieel/bittorrent-client/pkg/bitfield"
	messaging "github.com/agaabrieel/bittorrent-client/pkg/messaging"
	metainfo "github.com/agaabrieel/bittorrent-client/pkg/metainfo"
	piece "github.com/agaabrieel/bittorrent-client/pkg/piece"
)

type PeerOrchestrator struct {
	id             string
	Router         *messaging.Router
	clientId       [20]byte
	clientInfohash [20]byte
	AskedBlocks    []piece.Block
	Bitfield       bitfield.BitfieldMask
	Mutex          *sync.RWMutex
	Waitgroup      *sync.WaitGroup
	SendCh         chan<- messaging.Message
	RecvCh         <-chan messaging.Message
	ErrorCh        chan<- error
}

func NewPeerOrchestrator(meta *metainfo.TorrentMetainfo, r *messaging.Router, clientId [20]byte) (*PeerOrchestrator, error) {

	id, ch := "peer_orchestrator", make(chan messaging.Message, 1024)
	err := r.RegisterComponent(id, ch)
	if err != nil {
		return nil, fmt.Errorf("failed to register component with id %w: %v", id, err)
	}

	return &PeerOrchestrator{
		id:             id,
		clientId:       clientId,
		clientInfohash: meta.Infohash,
		Bitfield:       bitfield.NewBitfield(uint32(float64((meta.InfoDict.Length / meta.InfoDict.PieceLength) / 8))),
		RecvCh:         ch,
		Mutex:          &sync.RWMutex{},
		Waitgroup:      &sync.WaitGroup{},
	}, nil
}

func (mngr *PeerOrchestrator) Run(ctx context.Context, wg *sync.WaitGroup) {

	defer wg.Done()
	childCtx, ctxCancel := context.WithCancel(ctx)

	for {
		select {
		case msg := <-mngr.RecvCh:
			switch msg.PayloadType {
			case messaging.PeerDiscovered:

				payload, ok := msg.Payload.(messaging.PeerDiscoveredPayload)
				if !ok {
					mngr.ErrorCh <- errors.New("payload has incorrect type")
					continue
				}

				wg.Add(1)
				go func() {
					defer wg.Done()
					peerMngr := NewPeerManager(mngr.Router, nil, payload.Addr)
					go peerMngr.startPeerHandshake(childCtx, mngr.clientInfohash, mngr.clientId)
				}()

			case messaging.PeerConnected:
				payload, ok := msg.Payload.(messaging.PeerConnectedPayload)
				if !ok {
					mngr.ErrorCh <- errors.New("payload has incorrect type")
					continue
				}

				wg.Add(1)
				go func() {
					defer wg.Done()
					peerMngr := NewPeerManager(mngr.Router, payload.Conn, payload.Conn.RemoteAddr())
					go peerMngr.replyToPeerHandshake(childCtx, mngr.clientInfohash, mngr.clientId)
				}()
			}

		case <-ctx.Done():
			ctxCancel()
			return // should add logging here
		}
	}
}
