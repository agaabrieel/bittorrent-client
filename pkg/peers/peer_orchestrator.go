package peer

import (
	"context"
	"fmt"
	"sync"
	"time"

	messaging "github.com/agaabrieel/bittorrent-client/pkg/messaging"
	metainfo "github.com/agaabrieel/bittorrent-client/pkg/metainfo"
	"github.com/bits-and-blooms/bitset"
	"github.com/google/uuid"
)

type PeerOrchestrator struct {
	id             string
	Router         *messaging.Router
	clientId       [20]byte
	clientInfohash [20]byte
	Bitfield       *bitset.BitSet
	PeerBitfields  map[string]*bitset.BitSet
	SentMessages   map[string]bool
	Mutex          *sync.RWMutex
	Waitgroup      *sync.WaitGroup
	RecvCh         <-chan messaging.Message
}

func NewPeerOrchestrator(meta *metainfo.TorrentMetainfo, r *messaging.Router, clientId [20]byte) (*PeerOrchestrator, error) {

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
		PeerBitfields:  make(map[string]*bitset.BitSet),
		RecvCh:         ch,
		Mutex:          &sync.RWMutex{},
	}, nil
}

func (mngr *PeerOrchestrator) Run(ctx context.Context, wg *sync.WaitGroup) {

	childCtx, ctxCancel := context.WithCancel(ctx)
	defer ctxCancel()
	defer wg.Done()

	for {
		select {
		case msg := <-mngr.RecvCh:

			if msg.ReplyingTo != "" {
				mngr.Mutex.Lock()
				exists := mngr.SentMessages[msg.ReplyingTo]
				if !exists {
					mngr.Router.Send("peer_orchestrator", messaging.Message{
						SourceId:    mngr.id,
						PayloadType: messaging.Error,
						Payload: messaging.ErrorPayload{
							Message:     fmt.Sprintf("unexpected message with type %v replying to %s", msg.PayloadType, msg.ReplyingTo),
							Severity:    messaging.Warning,
							ErrorCode:   messaging.ErrCodeUnexpectedMessage,
							Time:        time.Now(),
							ComponentId: mngr.id,
						},
						CreatedAt: time.Now(),
					})
					continue
				}
				delete(mngr.SentMessages, msg.ReplyingTo)
				mngr.Mutex.Unlock()
			}

			if msg.ReplyTo != "" {
				mngr.Router.Send(msg.ReplyTo, messaging.Message{
					Id:          uuid.NewString(),
					SourceId:    mngr.id,
					ReplyTo:     "",
					ReplyingTo:  "",
					PayloadType: messaging.Acknowledged,
					Payload:     nil,
					CreatedAt:   time.Now(),
				})
			}

			switch msg.PayloadType {
			case messaging.PeersDiscovered:

				payload, ok := msg.Payload.(messaging.PeersDiscoveredPayload)
				if !ok {
					mngr.Router.Send(msg.ReplyTo, messaging.Message{
						SourceId:    mngr.id,
						PayloadType: messaging.Error,
						Payload: messaging.ErrorPayload{
							Message:     fmt.Sprintf("unexpected payload, expected PeersDiscoveredPayload, got %v", msg.PayloadType),
							Severity:    messaging.Warning,
							ErrorCode:   messaging.ErrCodeInvalidPayload,
							Time:        time.Now(),
							ComponentId: mngr.id,
						},
						CreatedAt: time.Now(),
					})
				}

				for _, addr := range payload.Addrs {
					peerMngr := NewPeerManager(mngr.Router, nil, addr, wg)
					go peerMngr.startPeerHandshake(childCtx, mngr.clientInfohash, mngr.clientId)
				}

			case messaging.PeerConnected:

				payload, ok := msg.Payload.(messaging.PeerConnectedPayload)
				if !ok {
					mngr.Router.Send("peer_orchestrator", messaging.Message{
						SourceId:    mngr.id,
						PayloadType: messaging.Error,
						Payload: messaging.ErrorPayload{
							Message:     fmt.Sprintf("unexpected payload, expected PeerConnectedPayload, got %v", msg.PayloadType),
							Severity:    messaging.Warning,
							ErrorCode:   messaging.ErrCodeInvalidPayload,
							Time:        time.Now(),
							ComponentId: mngr.id,
						},
						CreatedAt: time.Now(),
					})
				}

				peerMngr := NewPeerManager(mngr.Router, payload.Conn, payload.Conn.RemoteAddr(), wg)
				go peerMngr.replyToPeerHandshake(childCtx, mngr.clientInfohash, mngr.clientId)

			case messaging.PieceValidated:

				payload, ok := msg.Payload.(messaging.PieceValidatedPayload)
				if !ok {
					mngr.Router.Send("peer_orchestrator", messaging.Message{
						SourceId:    mngr.id,
						PayloadType: messaging.Error,
						Payload: messaging.ErrorPayload{
							Message:     fmt.Sprintf("unexpected payload, expected PieceValidatedPayload, got %v", msg.PayloadType),
							Severity:    messaging.Warning,
							ErrorCode:   messaging.ErrCodeInvalidPayload,
							Time:        time.Now(),
							ComponentId: mngr.id,
						},
						CreatedAt: time.Now(),
					})
				}

				wg.Add(1)
				go func() {
					mngr.Mutex.Lock()
					defer mngr.Mutex.Unlock()
					defer wg.Done()
					mngr.Bitfield.Set(uint(payload.Index))
				}()

			case messaging.PeerBitfield:

				payload, ok := msg.Payload.(messaging.PeerBitfieldPayload)
				if !ok {
					mngr.Router.Send("peer_orchestrator", messaging.Message{
						SourceId:    mngr.id,
						PayloadType: messaging.Error,
						Payload: messaging.ErrorPayload{
							Message:     fmt.Sprintf("unexpected payload, expected PeerBitfieldPayload, got %v", msg.PayloadType),
							Severity:    messaging.Warning,
							ErrorCode:   messaging.ErrCodeInvalidPayload,
							Time:        time.Now(),
							ComponentId: mngr.id,
						},
						CreatedAt: time.Now(),
					})
				}

				mngr.Mutex.Lock()
				mngr.PeerBitfields[msg.SourceId] = payload.Bitfield
				mngr.Mutex.Unlock()

			case messaging.PeerBitfieldUpdate:

				payload, ok := msg.Payload.(messaging.PeerBitfieldUpdatePayload)
				if !ok {
					mngr.Router.Send("peer_orchestrator", messaging.Message{
						SourceId:    mngr.id,
						PayloadType: messaging.Error,
						Payload: messaging.ErrorPayload{
							Message:     fmt.Sprintf("unexpected payload, expected PeerBitfieldUpdatePayload, got %v", msg.PayloadType),
							Severity:    messaging.Warning,
							ErrorCode:   messaging.ErrCodeInvalidPayload,
							Time:        time.Now(),
							ComponentId: mngr.id,
						},
						CreatedAt: time.Now(),
					})
				}

				mngr.Mutex.Lock()
				mngr.PeerBitfields[msg.SourceId].Set(uint(payload.Index))
				mngr.Mutex.Unlock()

			case messaging.NextPieceIndexRequest:

				_, ok := msg.Payload.(messaging.NextPieceIndexRequestPayload)
				if !ok {
					mngr.Router.Send("peer_orchestrator", messaging.Message{
						SourceId:    mngr.id,
						PayloadType: messaging.Error,
						Payload: messaging.ErrorPayload{
							Message:     fmt.Sprintf("unexpected payload, expected NextPieceIndexRequestPayload, got %v", msg.PayloadType),
							Severity:    messaging.Warning,
							ErrorCode:   messaging.ErrCodeInvalidPayload,
							Time:        time.Now(),
							ComponentId: mngr.id,
						},
						CreatedAt: time.Now(),
					})
				}

				// PICK NEXT PIECE BASED ON RARITY
				// mockup
				index := 10
				// MOCKUP

				msgId := uuid.NewString()
				mngr.SentMessages[msgId] = true

				mngr.Router.Send(msg.ReplyTo, messaging.Message{
					Id:          msgId,
					SourceId:    mngr.id,
					ReplyTo:     mngr.id,
					ReplyingTo:  msg.Id,
					PayloadType: messaging.NextPieceIndexSend,
					Payload: messaging.NextPieceIndexSendPayload{
						Index: index,
					},
					CreatedAt: time.Now(),
				})

			}

		case <-ctx.Done():
			return // should add logging here
		}
	}
}
