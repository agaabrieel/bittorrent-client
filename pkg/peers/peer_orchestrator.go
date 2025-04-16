package peer

import (
	"context"
	"errors"
	"sync"

	bitfield "github.com/agaabrieel/bittorrent-client/pkg/bitfield"
	messaging "github.com/agaabrieel/bittorrent-client/pkg/messaging"
	metainfo "github.com/agaabrieel/bittorrent-client/pkg/metainfo"
	piece "github.com/agaabrieel/bittorrent-client/pkg/piece"
)

type PeerOrchestrator struct {
	id             string
	clientId       [20]byte
	clientInfohash [20]byte
	AskedBlocks    []piece.Block
	Bitfield       bitfield.BitfieldMask
	Mutex          *sync.RWMutex
	Waitgroup      *sync.WaitGroup
	SendCh         chan<- messaging.DirectedMessage
	RecvCh         <-chan messaging.DirectedMessage
	ErrorCh        chan<- error
}

func NewPeerOrchestrator(meta metainfo.TorrentMetainfo, r *messaging.Router, clientId [20]byte) *PeerOrchestrator {

	id, ch := r.NewComponent()

	return &PeerOrchestrator{
		id:             id,
		clientId:       clientId,
		clientInfohash: meta.Infohash,
		Bitfield:       bitfield.NewBitfield(uint32(float64((meta.InfoDict.Length / meta.InfoDict.PieceLength) / 8))),
		RecvCh:         ch,
		Mutex:          &sync.RWMutex{},
		Waitgroup:      &sync.WaitGroup{},
	}
}

func (mngr *PeerOrchestrator) Run(ctx context.Context, wg *sync.WaitGroup) {

	defer wg.Done()
	childCtx, ctxCancel := context.WithCancel(ctx)

	for {
		select {
		case msg := <-mngr.RecvCh:
			switch msg.MessageType {
			case messaging.NewPeerFromTracker, messaging.NewPeerConnection:

				peerMngr, ok := msg.Data.(PeerManager)
				if !ok {
					mngr.ErrorCh <- errors.New("payload has incorrect type")
					continue
				}

				wg.Add(1)
				go func() {

					defer wg.Done()

					if msg.MessageType == messaging.NewPeerFromTracker {
						peerMngr.WaitGroup.Add(1)
						go peerMngr.startPeerHandshake(childCtx, mngr.ErrorCh, mngr.clientInfohash, mngr.clientId)
					} else {
						peerMngr.WaitGroup.Add(1)
						go peerMngr.replyToPeerHandshake(childCtx, mngr.ErrorCh, mngr.clientInfohash, mngr.clientId)
					}
				}()

			case messaging.FileFinished:
				ctxCancel()
				return
			}

		case <-ctx.Done():
			ctxCancel()
			return // should add logging here
		}
	}
}
