package piece

import (
	"context"
	"crypto/sha1"
	"fmt"
	"sync"
	"time"

	messaging "github.com/agaabrieel/bittorrent-client/pkg/messaging"
	metainfo "github.com/agaabrieel/bittorrent-client/pkg/metainfo"
	bitset "github.com/bits-and-blooms/bitset"
	"github.com/google/uuid"
)

const SHA1Size int = 20

type PieceManager struct {
	id           string
	ClientId     [20]byte
	Router       *messaging.Router
	PieceCache   *PieceCache
	Metainfo     *metainfo.TorrentMetainfoInfoDict
	Bitfield     *bitset.BitSet
	SentMessages map[string]bool
	RecvCh       <-chan messaging.Message
	mutex        *sync.RWMutex
}

func NewPieceManager(meta *metainfo.TorrentMetainfo, r *messaging.Router, clientId [20]byte) (*PieceManager, error) {

	id, ch := "piece_manager", make(chan messaging.Message, 1024)
	err := r.RegisterComponent(id, ch)
	if err != nil {
		return nil, fmt.Errorf("failed to register component with id %s: %w", id, err)
	}

	bitfieldSize := len(meta.InfoDict.Pieces) / SHA1Size
	cacheCapacity := PIECE_BUFFER_MAX_SIZE / meta.InfoDict.PieceLength

	return &PieceManager{
		id:           id,
		ClientId:     clientId,
		Router:       r,
		PieceCache:   NewPieceCache(uint32(cacheCapacity)),
		SentMessages: make(map[string]bool),
		Metainfo:     meta.InfoDict,
		Bitfield:     bitset.New(uint(bitfieldSize)),
		RecvCh:       ch,
		mutex:        &sync.RWMutex{},
	}, nil
}

func (mngr *PieceManager) Run(ctx context.Context, wg *sync.WaitGroup) {

	defer wg.Done()
	defer mngr.PieceCache.Cleanup()

	_, cancel := context.WithCancel(ctx)
	defer cancel()

	for {
		select {
		case msg := <-mngr.RecvCh:

			if msg.ReplyingTo != "" {

				mngr.mutex.Lock()
				exists := mngr.SentMessages[msg.ReplyingTo]
				if !exists {
					mngr.Router.Send("error_manager", messaging.Message{
						Id:          uuid.NewString(),
						SourceId:    mngr.id,
						PayloadType: messaging.Error,
						Payload: messaging.ErrorPayload{
							Message:     fmt.Sprintf("unexpected message with type %v replying to %s", msg.PayloadType, msg.ReplyingTo),
							Severity:    messaging.Warning,
							Time:        time.Now(),
							ComponentId: mngr.id,
						},
						CreatedAt: time.Now(),
					})
					continue
				}
				delete(mngr.SentMessages, msg.ReplyingTo)
				mngr.mutex.Unlock()
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

			case messaging.BlockRequest:
				_, ok := msg.Payload.(messaging.BlockRequestPayload)
				if !ok {
					mngr.Router.Send("error_manager", messaging.Message{
						Id:          uuid.NewString(),
						SourceId:    mngr.id,
						PayloadType: messaging.Error,
						Payload: messaging.ErrorPayload{
							Message:     fmt.Sprintf("incorrect payload type: expected BlockRequestPayload, got %v", msg.PayloadType),
							ErrorCode:   messaging.ErrCodeInvalidPayload,
							ComponentId: "piece_manager",
							Time:        time.Now(),
							Severity:    messaging.Warning,
						},
						CreatedAt: time.Now(),
					})
					return
				}

				mngr.handleBlockRequestMessage(msg)

			case messaging.BlockSend:
				_, ok := msg.Payload.(messaging.BlockSendPayload)
				if !ok {
					mngr.Router.Send("error_manager", messaging.Message{
						Id:          uuid.NewString(),
						SourceId:    mngr.id,
						PayloadType: messaging.Error,
						Payload: messaging.ErrorPayload{
							Message:     fmt.Sprintf("incorrect payload type: expected BlockSendPayload, got %v", msg.PayloadType),
							ErrorCode:   messaging.ErrCodeInvalidPayload,
							ComponentId: "piece_manager",
							Time:        time.Now(),
							Severity:    messaging.Warning,
						},
						CreatedAt: time.Now(),
					})
					return
				}

				mngr.handleBlockSendMessage(msg)

			case messaging.NextPieceIndexRequest:
				_, ok := msg.Payload.(messaging.NextBlockIndexRequestPayload)
				if !ok {
					mngr.Router.Send("error_manager", messaging.Message{
						Id:          uuid.NewString(),
						SourceId:    mngr.id,
						PayloadType: messaging.Error,
						Payload: messaging.ErrorPayload{
							Message:     fmt.Sprintf("incorrect payload type: expected NextBlockIndexRequestPayload, got %v", msg.PayloadType),
							ErrorCode:   messaging.ErrCodeInvalidPayload,
							ComponentId: "piece_manager",
							Time:        time.Now(),
							Severity:    messaging.Warning,
						},
						CreatedAt: time.Now(),
					})
					return
				}
				mngr.processNextBlockRequest(msg)
			}

		case <-ctx.Done():
			return
		}
	}
}

func (mngr *PieceManager) processNextBlockRequest(msg messaging.Message) {

	payload := msg.Payload.(messaging.NextBlockIndexRequestPayload)

	offset, size, err := mngr.pickNextBlock(uint32(payload.PieceIndex))
	if err != nil {
		mngr.Router.Send("error_manager", messaging.Message{
			Id:          uuid.NewString(),
			SourceId:    mngr.id,
			PayloadType: messaging.Error,
			Payload: messaging.ErrorPayload{
				Message:     fmt.Sprintf("failed to pick next block: %s", err.Error()),
				ErrorCode:   messaging.ErrCodeInvalidBlock,
				ComponentId: "piece_manager",
				Time:        time.Now(),
				Severity:    messaging.Warning,
			},
			CreatedAt: time.Now(),
		})
		return
	}

	msgId := uuid.NewString()

	mngr.mutex.Lock()
	mngr.SentMessages[msgId] = true
	mngr.mutex.Unlock()

	mngr.Router.Send(msg.ReplyTo, messaging.Message{
		Id:          msgId,
		SourceId:    mngr.id,
		ReplyTo:     mngr.id,
		ReplyingTo:  msg.Id,
		PayloadType: messaging.NextBlockIndexSend,
		Payload: messaging.NextBlockIndexSendPayload{
			PieceIndex: payload.PieceIndex,
			Offset:     offset,
			Size:       size,
		},
	})
}

func (mngr *PieceManager) pickNextBlock(pieceIndex uint32) (int, int, error) {

	mngr.mutex.Lock()
	defer mngr.mutex.Unlock()

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

func (mngr *PieceManager) handleBlockRequestMessage(msg messaging.Message) {

	var blockData []byte
	var err error

	mngr.mutex.Lock()
	defer mngr.mutex.Unlock()

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
			Id:          msgId,
			SourceId:    mngr.id,
			ReplyTo:     mngr.id,
			ReplyingTo:  msg.Id,
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
			Id:          msgId,
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

func (mngr *PieceManager) handleBlockSendMessage(msg messaging.Message) {

	mngr.mutex.Lock()
	defer mngr.mutex.Unlock()

	data, ok := msg.Payload.(messaging.BlockSendPayload)
	if !ok {
		mngr.Router.Send("error_manager", messaging.Message{
			Id:          uuid.NewString(),
			SourceId:    mngr.id,
			PayloadType: messaging.Error,
			Payload: messaging.ErrorPayload{
				Message:     fmt.Sprintf("incorrect payload type: expected BlockSendPayload, got %v", msg.PayloadType),
				ErrorCode:   messaging.ErrCodeInvalidPayload,
				ComponentId: "processBlockSendGoroutine",
				Time:        time.Now(),
				Severity:    messaging.Warning,
			},
			CreatedAt: time.Now(),
		})
		return
	}

	mngr.PieceCache.mu.Lock()
	defer mngr.PieceCache.mu.Unlock()

	mngr.PieceCache.PutBlock(data.Data, data.Index, data.Offset)

	isPieceComplete, err := mngr.PieceCache.isPieceComplete(data.Index, uint32(mngr.Metainfo.PieceLength))
	if err != nil {
		mngr.Router.Send("error_manager", messaging.Message{
			Id:          uuid.NewString(),
			SourceId:    mngr.id,
			PayloadType: messaging.Error,
			Payload: messaging.ErrorPayload{
				Message:     fmt.Sprintf("failed to check if piece %d is complete", data.Index),
				ErrorCode:   messaging.ErrCodeInvalidPiece,
				ComponentId: "processBlockSendGoroutine",
				Time:        time.Now(),
				Severity:    messaging.Warning,
			},
			CreatedAt: time.Now(),
		})
		return
	}

	if !mngr.validatePiece(data.Index) {
		mngr.Router.Send("error_manager", messaging.Message{
			Id:          uuid.NewString(),
			SourceId:    mngr.id,
			PayloadType: messaging.Error,
			Payload: messaging.ErrorPayload{
				Message:     fmt.Sprintf("piece %d is invalid", data.Index),
				ErrorCode:   messaging.ErrCodeInvalidPiece,
				ComponentId: "processBlockSendGoroutine",
				Time:        time.Now(),
				Severity:    messaging.Warning,
			},
			CreatedAt: time.Now(),
		})

		mngr.Router.Broadcast(messaging.Message{
			Id:          uuid.NewString(),
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
			mngr.Router.Send("error_manager", messaging.Message{
				Id:          uuid.NewString(),
				SourceId:    mngr.id,
				PayloadType: messaging.Error,
				Payload: messaging.ErrorPayload{
					Message:     fmt.Sprintf("failed to process block for piece %d", data.Index),
					ErrorCode:   messaging.ErrCodeInvalidPiece,
					ComponentId: "processBlockSendGoroutine",
					Time:        time.Now(),
					Severity:    messaging.Warning,
				},
				CreatedAt: time.Now(),
			})
			return
		}

		mngr.Bitfield.Set(uint(data.Index))

		mngr.Router.Broadcast(messaging.Message{
			Id:          uuid.NewString(),
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
			Id:          msgId,
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

	mngr.mutex.RLock()
	defer mngr.mutex.RUnlock()

	mngr.PieceCache.mu.Lock()
	defer mngr.PieceCache.mu.Unlock()

	piece, err := mngr.PieceCache.GetPiece(pieceIndex)
	if err != nil {
		// log
		return false
	}

	return sha1.Sum(piece.Blocks) == [20]byte(mngr.Metainfo.Pieces[20*pieceIndex:20*pieceIndex+20])
}
