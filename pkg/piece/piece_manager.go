package piece

import (
	"context"
	"crypto/sha1"
	"fmt"
	"math"
	"math/bits"
	"math/rand"
	"sync"
	"time"

	bitfield "github.com/agaabrieel/bittorrent-client/pkg/bitfield"
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
	Bitfield   bitfield.BitfieldMask
	RecvCh     <-chan messaging.Message
	mu         *sync.RWMutex
}

func NewPieceManager(meta *metainfo.TorrentMetainfo, r *messaging.Router, clientId [20]byte) (*PieceManager, error) {

	id, ch := "piece_manager", make(chan messaging.Message, 1024)
	err := r.RegisterComponent(id, ch)
	if err != nil {
		return nil, fmt.Errorf("failed to register component with id %w: %v", id, err)
	}

	bitfieldSize := int(math.Ceil((math.Ceil(float64(meta.InfoDict.Length) / float64(meta.InfoDict.PieceLength))) / 8))
	cacheCapacity := PIECE_BUFFER_MAX_SIZE / meta.InfoDict.PieceLength

	bs := bitset.New(uint(bitfieldSize))

	return &PieceManager{
		id:         id,
		ClientId:   clientId,
		Router:     r,
		PieceCache: NewPieceCache(uint32(cacheCapacity)),
		Metainfo:   meta.InfoDict,
		Bitfield:   make(bitfield.BitfieldMask, bitfieldSize),
		RecvCh:     ch,
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
	mngr.mu.Lock()
	defer mngr.mu.Unlock()

	var blockIndex int
	var bitIndex int
	var bitfieldByte byte
	for {
		blockIndex = rand.Intn(BLOCK_BITFIELD_SIZE)
		bitfieldByte = mngr.Pieces[pieceIndex].BlockBitfield[blockIndex]

		if bitfieldByte == 0 {
			continue
		}

		bitIndex = bits.Len(uint(bitfieldByte)) - 1
		// start: pieceLen * pieceIndex, offset: blockSize * (blockIndex + bitIndex)
		mngr.SendCh <- messaging.DirectedMessage{
			MessageType: messaging.BlockSend,
			Data:        []byte{byte(mngr.Metainfo.PieceLength), byte(BLOCK_SIZE * (blockIndex + bitIndex))},
		}
	}

}

func (mngr *PieceManager) processBlockRequest(msg messaging.Message) {

	mngr.mu.Lock()
	defer mngr.mu.Unlock()

	data, ok := msg.Payload.(messaging.BlockRequestPayload)
	if !ok {
		// log
		return
	}

	mngr.PieceCache.mu.Lock()
	defer mngr.PieceCache.mu.Unlock()
	blockData, err := mngr.PieceCache.GetBlock(data.Index, data.Offset)
	if err != nil {
		// log
		return
	}

	mngr.Router.Send(msg.SourceId, messaging.Message{
		SourceId:    mngr.id,
		PayloadType: messaging.BlockSend,
		Payload: messaging.BlockSendPayload{
			Index:  data.Index,
			Offset: data.Offset,
			Size:   uint32(len(blockData)),
			Data:   blockData,
		},
		CreatedAt: time.Now(),
	})

}

func (mngr *PieceManager) processBlockSend(msg messaging.Message) {

	mngr.mu.Lock()
	defer mngr.mu.Unlock()

	data, ok := msg.Payload.(messaging.BlockSendPayload)
	if !ok {
		return
	}

	mngr.PieceCache.mu.Lock()
	defer mngr.PieceCache.mu.Unlock()
	mngr.PieceCache.PutBlock(data.Data, data.Index, data.Offset)
}

func (mngr *PieceManager) validatePiece(pieceData []byte, pieceIndex uint32) bool {

	mngr.mu.RLock()
	defer mngr.mu.RUnlock()

	metainfoHash := [20]byte(mngr.Metainfo.Pieces[20*pieceIndex : 20*pieceIndex+20])
	recvHash := sha1.Sum(pieceData)

	return recvHash == metainfoHash
}
