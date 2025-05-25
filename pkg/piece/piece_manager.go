package piece

import (
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	apperrors "github.com/agaabrieel/bittorrent-client/pkg/errors"
	messaging "github.com/agaabrieel/bittorrent-client/pkg/messaging"
	metainfo "github.com/agaabrieel/bittorrent-client/pkg/metainfo"
	bitset "github.com/bits-and-blooms/bitset"
	"github.com/google/uuid"
)

type PieceManager struct {
	id           string
	ClientId     [20]byte
	Router       *messaging.Router
	PieceCache   *PieceCache
	Metainfo     *metainfo.TorrentMetainfoInfoDict
	Bitfield     *bitset.BitSet
	SentMessages map[string]bool
	RecvCh       <-chan messaging.Message
	ErrCh        chan<- apperrors.Error
	mu           *sync.RWMutex
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

	defer wg.Done()
	defer mngr.PieceCache.Cleanup()

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	for {
		select {
		case msg := <-mngr.RecvCh:

			if msg.ReplyingTo != "" {
				mngr.mu.Lock()
				exists := mngr.SentMessages[msg.ReplyingTo]
				if !exists {
					mngr.ErrCh <- apperrors.Error{
						Err:         errors.New("unexpected message"),
						Message:     fmt.Sprintf("unexpected message with type %v replying to %s", msg.PayloadType, msg.ReplyingTo),
						Severity:    apperrors.Warning,
						Time:        time.Now(),
						ComponentId: mngr.id,
					}
					continue
				}
				delete(mngr.SentMessages, msg.ReplyingTo)
				mngr.mu.Unlock()
			}

			if msg.ReplyTo != "" {
				mngr.Router.Send(msg.ReplyTo, messaging.Message{
					MsgId:       uuid.NewString(),
					SourceId:    mngr.id,
					ReplyTo:     "",
					ReplyingTo:  "",
					PayloadType: messaging.Acknowledged,
					Payload:     nil,
					CreatedAt:   time.Now(),
				})
			}

			switch msg.PayloadType {

			case messaging.BlockRequest:
				_, ok := msg.Payload.(messaging.BlockRequestPayload)
				if !ok {
					mngr.ErrCh <- apperrors.Error{
						Err:         fmt.Errorf("incorrect payload type"),
						Message:     "incorrect payload type",
						ErrorCode:   apperrors.ErrCodeInvalidPayload,
						ComponentId: "piece_manager",
						Time:        time.Now(),
						Severity:    apperrors.Warning,
					}
					return
				}
				go mngr.processBlockRequest(childCtx, msg)

			case messaging.BlockSend:
				_, ok := msg.Payload.(messaging.BlockSendPayload)
				if !ok {
					mngr.ErrCh <- apperrors.Error{
						Err:         fmt.Errorf("incorrect payload type"),
						Message:     "incorrect payload type",
						ErrorCode:   apperrors.ErrCodeInvalidPayload,
						ComponentId: "piece_manager",
						Time:        time.Now(),
						Severity:    apperrors.Warning,
					}
					return
				}
				go mngr.processBlockSend(childCtx, msg)

			case messaging.NextPieceIndexRequest:
				_, ok := msg.Payload.(messaging.NextBlockIndexRequestPayload)
				if !ok {
					mngr.ErrCh <- apperrors.Error{
						Err:         fmt.Errorf("incorrect payload type"),
						Message:     "incorrect payload type",
						ErrorCode:   apperrors.ErrCodeInvalidPayload,
						ComponentId: "piece_manager",
						Time:        time.Now(),
						Severity:    apperrors.Warning,
					}
					return
				}
				go mngr.processNextBlockRequest(childCtx, msg)
			}

		case <-ctx.Done():
			return
		}
	}
}

func (mngr *PieceManager) processNextBlockRequest(ctx context.Context, msg messaging.Message) {

	resultCh := make(chan bool, 1)

	go func() {
		payload := msg.Payload.(messaging.NextBlockIndexRequestPayload)

		offset, size, err := mngr.pickNextBlock(uint32(payload.PieceIndex))
		if err != nil {
			mngr.ErrCh <- apperrors.Error{
				Err:         err,
				Message:     "failed to pick next block",
				ErrorCode:   apperrors.ErrCodeInvalidBlock,
				ComponentId: "piece_manager",
				Time:        time.Now(),
				Severity:    apperrors.Warning,
			}
			return
		}

		msgId := uuid.NewString()

		mngr.mu.Lock()
		mngr.SentMessages[msgId] = true
		mngr.mu.Unlock()

		mngr.Router.Send(msg.ReplyTo, messaging.Message{
			MsgId:       msgId,
			SourceId:    mngr.id,
			ReplyTo:     mngr.id,
			ReplyingTo:  msg.MsgId,
			PayloadType: messaging.NextBlockIndexSend,
			Payload: messaging.NextBlockIndexSendPayload{
				PieceIndex: payload.PieceIndex,
				Offset:     offset,
				Size:       size,
			},
		})
		resultCh <- true
	}()

	select {
	case <-resultCh:
		close(resultCh)
		return
	case <-ctx.Done():
		mngr.ErrCh <- apperrors.Error{
			Err:         fmt.Errorf("context cancelled"),
			Message:     "context cancelled",
			ErrorCode:   apperrors.ErrCodeContextCancelled,
			ComponentId: "processNextBlockRequestGoroutine",
			Time:        time.Now(),
			Severity:    apperrors.Info,
		}
		return
	}
}

func (mngr *PieceManager) pickNextBlock(pieceIndex uint32) (int, int, error) {

	mngr.mu.Lock()
	defer mngr.mu.Unlock()

	piece, err := mngr.PieceCache.GetPiece(pieceIndex)
	if err != nil {
		// log, do something, return
		return -1, -1, err
	}

	mngr.PieceCache.mu.Lock()
	defer mngr.PieceCache.mu.Unlock()

	var blockIndex int
	for i, status := range piece.BlockStatuses {
		if status == Missing {
			blockIndex = i
			piece.BlockStatuses[i] = Pending
			break
		}
	}

	return blockIndex * BLOCK_SIZE, int(mngr.Metainfo.Length) - blockIndex*BLOCK_SIZE, nil
}

func (mngr *PieceManager) processBlockRequest(ctx context.Context, msg messaging.Message) {

	resultChan := make(chan bool, 1)

	go func() {

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

		msgId := uuid.NewString()
		mngr.SentMessages[msgId] = true

		blockData, err = mngr.PieceCache.GetBlock(data.Index, data.Offset, data.Size)
		if err != nil {

			mngr.Router.Send(msg.ReplyTo, messaging.Message{
				MsgId:       msgId,
				SourceId:    mngr.id,
				ReplyTo:     mngr.id,
				ReplyingTo:  msg.MsgId,
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
				MsgId:       msgId,
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

			mngr.ErrCh <- apperrors.Error{
				Err:         fmt.Errorf("failed to get block with offset %d (piece %d): %w", data.Offset, data.Index, err),
				Message:     fmt.Sprintf("failed to get block with offset %d (piece %d)", data.Offset, data.Index),
				ErrorCode:   apperrors.ErrCodeInvalidBlock,
				ComponentId: "processBlockRequestGoroutine",
				Time:        time.Now(),
				Severity:    apperrors.Warning,
			}

		}

		resultChan <- true
	}()

	select {
	case <-resultChan:
		close(resultChan)
		return
	case <-ctx.Done():
		mngr.ErrCh <- apperrors.Error{
			Err:         fmt.Errorf("context cancelled"),
			Message:     "context cancelled",
			ErrorCode:   apperrors.ErrCodeContextCancelled,
			ComponentId: "processBlockRequestGoroutine",
			Time:        time.Now(),
			Severity:    apperrors.Info,
		}
		return
	}

}

func (mngr *PieceManager) processBlockSend(ctx context.Context, msg messaging.Message) {

	resultChan := make(chan bool, 1)
	go func() {

		mngr.mu.Lock()
		defer mngr.mu.Unlock()

		data, ok := msg.Payload.(messaging.BlockSendPayload)
		if !ok {
			mngr.ErrCh <- apperrors.Error{
				Err:         fmt.Errorf("incorrect payload type"),
				Message:     "incorrect payload type",
				ErrorCode:   apperrors.ErrCodeInvalidPayload,
				ComponentId: "processBlockSendGoroutine",
				Time:        time.Now(),
				Severity:    apperrors.Warning,
			}
			return
		}

		mngr.PieceCache.mu.Lock()
		defer mngr.PieceCache.mu.Unlock()

		mngr.PieceCache.PutBlock(data.Data, data.Index, data.Offset)

		isPieceComplete, err := mngr.PieceCache.isPieceComplete(data.Index, uint32(mngr.Metainfo.PieceLength))
		if err != nil {
			mngr.ErrCh <- apperrors.Error{
				Err:         fmt.Errorf("failed to check if piece %d is complete: %w", data.Index, err),
				Message:     fmt.Sprintf("failed to check if piece %d is complete", data.Index),
				ErrorCode:   apperrors.ErrCodeInvalidPiece,
				ComponentId: "processBlockSendGoroutine",
				Time:        time.Now(),
				Severity:    apperrors.Warning,
			}
			return
		}

		if !mngr.validatePiece(data.Index) {
			mngr.ErrCh <- apperrors.Error{
				Err:         fmt.Errorf("piece %d is invalid", data.Index),
				Message:     fmt.Sprintf("piece %d is invalid", data.Index),
				ErrorCode:   apperrors.ErrCodeInvalidPiece,
				ComponentId: "processBlockSendGoroutine",
				Time:        time.Now(),
				Severity:    apperrors.Warning,
			}

			mngr.Router.Broadcast(messaging.Message{
				MsgId:       uuid.NewString(),
				SourceId:    mngr.id,
				ReplyTo:     "",
				PayloadType: messaging.PieceInvalidated,
				Payload: messaging.PieceInvalidatedPayload{
					Index: data.Index,
				},
				CreatedAt: time.Now(),
			})
			return
		}

		if isPieceComplete {

			piece, err := mngr.PieceCache.GetPiece(data.Index)
			if err != nil {
				mngr.ErrCh <- apperrors.Error{
					Err:         fmt.Errorf("failed to process block for piece %d: %w", data.Index, err),
					Message:     fmt.Sprintf("failed to process block for piece %d", data.Index),
					ErrorCode:   apperrors.ErrCodeInvalidPiece,
					ComponentId: "processBlockSendGoroutine",
					Time:        time.Now(),
					Severity:    apperrors.Warning,
				}
				return
			}

			mngr.Bitfield.Set(uint(data.Index))

			mngr.Router.Broadcast(messaging.Message{
				MsgId:       uuid.NewString(),
				SourceId:    mngr.id,
				ReplyTo:     mngr.id,
				PayloadType: messaging.PieceValidated,
				Payload: messaging.PieceValidatedPayload{
					Index: data.Index,
				},
				CreatedAt: time.Now(),
			})

			msgId := uuid.NewString()
			mngr.SentMessages[msgId] = true

			mngr.Router.Send("io_manager", messaging.Message{
				MsgId:       msgId,
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
		resultChan <- true
	}()

	select {
	case <-resultChan:
		close(resultChan)
		return
	case <-ctx.Done():
		mngr.ErrCh <- apperrors.Error{
			Err:         fmt.Errorf("context cancelled"),
			Message:     "context cancelled",
			ErrorCode:   apperrors.ErrCodeContextCancelled,
			ComponentId: "processBlockSendGoroutine",
			Time:        time.Now(),
			Severity:    apperrors.Info,
		}
		return
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
