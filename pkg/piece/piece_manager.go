package piece

import (
	"context"
	"crypto/sha1"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/agaabrieel/bittorrent-client/pkg/apperrors"
	messaging "github.com/agaabrieel/bittorrent-client/pkg/messaging"
	metainfo "github.com/agaabrieel/bittorrent-client/pkg/metainfo"
	bitset "github.com/bits-and-blooms/bitset"
)

type PieceManager struct {
	id         string
	ClientId   [20]byte
	Router     *messaging.Router
	PieceCache *PieceCache
	Metainfo   *metainfo.TorrentMetainfoInfoDict
	Bitfield   *bitset.BitSet
	RecvCh     <-chan messaging.Message
	ErrCh      chan<- apperrors.Error
	mu         *sync.RWMutex
}

func NewPieceManager(meta *metainfo.TorrentMetainfo, r *messaging.Router, errCh chan<- apperrors.Error, clientId [20]byte) (*PieceManager, error) {

	id, ch := "piece_manager", make(chan messaging.Message, 1024)
	err := r.RegisterComponent(id, ch)
	if err != nil {
		return nil, fmt.Errorf("failed to register component with id %s: %w", id, err)
	}

	bitfieldSize := int(math.Ceil((math.Ceil(float64(meta.InfoDict.Length) / float64(meta.InfoDict.PieceLength))) / 8))
	cacheCapacity := PIECE_BUFFER_MAX_SIZE / meta.InfoDict.PieceLength

	return &PieceManager{
		id:         id,
		ClientId:   clientId,
		Router:     r,
		PieceCache: NewPieceCache(uint32(cacheCapacity)),
		Metainfo:   meta.InfoDict,
		Bitfield:   bitset.New(uint(bitfieldSize)),
		RecvCh:     ch,
		ErrCh:      errCh,
		mu:         &sync.RWMutex{},
	}, nil
}

func (mngr *PieceManager) Run(ctx context.Context, wg *sync.WaitGroup) {

	defer wg.Wait()
	defer mngr.PieceCache.Cleanup()

	for {
		select {
		case msg := <-mngr.RecvCh:
			switch msg.PayloadType {

			case messaging.BlockRequest:
				_, ok := msg.Payload.(messaging.BlockRequestPayload)
				if !ok {
					// LOG/RETURN/WHATEVER
					return
				}
				go mngr.processBlockRequest(msg)
			case messaging.BlockSend:
				_, ok := msg.Payload.(messaging.BlockSendPayload)
				if !ok {
					// mngr.ErrCh <- errors.New("incorrect payload type")
					// log, whatever
					return
				}
				go mngr.processBlockSend(msg)
			default:
				// LOG, WHATEVER
			}

		case <-ctx.Done():
			return

		default:
			// LOG, WHATEVER
		}
	}
}

func (mngr *PieceManager) pickNextPiece() {
}

func (mngr *PieceManager) pickNextBlock(pieceIndex uint32) {
}

func (mngr *PieceManager) processBlockRequest(msg messaging.Message) {

	var blockData []byte
	var err error

	mngr.mu.Lock()
	defer mngr.mu.Unlock()

	data, ok := msg.Payload.(messaging.BlockRequestPayload)
	if !ok {
		// log
		return
	}

	mngr.PieceCache.mu.Lock()
	defer mngr.PieceCache.mu.Unlock()

	blockData, err = mngr.PieceCache.GetBlock(data.Index, data.Offset, data.Size)
	if err != nil {

		mngr.Router.Send(msg.ReplyTo, messaging.Message{
			SourceId:    mngr.id,
			ReplyTo:     mngr.id,
			PayloadType: messaging.BlockSend,
			Payload: messaging.BlockSendPayload{
				Index:  data.Index,
				Offset: data.Offset,
				Size:   uint32(len(blockData)),
				Data:   blockData,
			},
			CreatedAt: time.Now(),
		})

	} else {

		mngr.Router.Send("io_manager", messaging.Message{
			SourceId:    mngr.id,
			ReplyTo:     msg.ReplyTo,
			PayloadType: messaging.BlockRequest,
			Payload: messaging.BlockRequestPayload{
				Index:  data.Index,
				Offset: data.Offset,
				Size:   data.Size,
			},
			CreatedAt: time.Now(),
		})

	}
}

func (mngr *PieceManager) processBlockSend(msg messaging.Message) {

	mngr.mu.Lock()
	defer mngr.mu.Unlock()

	data, ok := msg.Payload.(messaging.BlockSendPayload)
	if !ok {
		// log
		return
	}

	mngr.PieceCache.mu.Lock()
	defer mngr.PieceCache.mu.Unlock()

	mngr.PieceCache.PutBlock(data.Data, data.Index, data.Offset)

	if isComplete, err := mngr.PieceCache.isPieceComplete(data.Index, uint32(mngr.Metainfo.PieceLength)); isComplete && err != nil && mngr.validatePiece(data.Index) {

		piece, err := mngr.PieceCache.GetPiece(data.Index)
		if err != nil {

		}

		mngr.Bitfield.Set(uint(data.Index))

		mngr.Router.Broadcast(messaging.Message{
			SourceId:    mngr.id,
			ReplyTo:     mngr.id,
			PayloadType: messaging.PieceValidated,
			Payload: messaging.PieceValidatedPayload{
				Index: data.Index,
			},
			CreatedAt: time.Now(),
		})

		mngr.Router.Send("io_manager", messaging.Message{
			SourceId:    mngr.id,
			ReplyTo:     mngr.id,
			PayloadType: messaging.PieceSend,
			Payload: messaging.PieceSendPayload{
				Index: data.Index,
				Size:  uint32(len(piece.Blocks)),
				Data:  piece.Blocks,
			},
			CreatedAt: time.Now(),
		})

	}
}

func (mngr *PieceManager) validatePiece(pieceIndex uint32) bool {

	mngr.mu.RLock()
	defer mngr.mu.RUnlock()

	mngr.PieceCache.mu.Lock()
	defer mngr.PieceCache.mu.Unlock()

	piece, err := mngr.PieceCache.GetPiece(pieceIndex)
	if err != nil {
		// log
		return false
	}

	return sha1.Sum(piece.Blocks) == [20]byte(mngr.Metainfo.Pieces[20*pieceIndex:20*pieceIndex+20])
}
