package piece

import (
	"context"
	"crypto/sha1"
	"fmt"
	"math"
	"math/bits"
	"math/rand"
	"sync"

	bitfield "github.com/agaabrieel/bittorrent-client/pkg/bitfield"
	messaging "github.com/agaabrieel/bittorrent-client/pkg/messaging"
	metainfo "github.com/agaabrieel/bittorrent-client/pkg/metainfo"
)

const BLOCK_SIZE = 16384                   // 16KB
const BLOCK_BITFIELD_SIZE = BLOCK_SIZE / 8 // 2048

type PieceManager struct {
	id                      string
	ClientId                [20]byte
	PieceToBlockBitfieldMap map[uint32]bitfield.BitfieldMask
	Metainfo                *metainfo.TorrentMetainfoInfoDict
	Bitfield                bitfield.BitfieldMask
	RecvCh                  <-chan messaging.Message
	mu                      *sync.Mutex
}

type BlockStatus uint8

const (
	Missing BlockStatus = iota
	Pending
	Complete
)

type Block struct {
	Index  uint32
	Offset uint32
	Data   []byte
	Status BlockStatus
}

type Piece struct {
	BlockBitfield bitfield.BitfieldMask
	Blocks        []Block
}

func NewPieceManager(meta *metainfo.TorrentMetainfo, r *messaging.Router, clientId [20]byte) (*PieceManager, error) {

	id, ch := "piece_manager", make(chan messaging.Message, 1024)
	err := r.RegisterComponent(id, ch)
	if err != nil {
		return nil, fmt.Errorf("failed to register component with id %w: %v", id, err)
	}

	bitfieldSize := int(math.Ceil((math.Ceil(float64(meta.InfoDict.Length) / float64(meta.InfoDict.PieceLength))) / 8))

	return &PieceManager{
		id:       id,
		ClientId: clientId,
		Metainfo: meta.InfoDict,
		Bitfield: make(bitfield.BitfieldMask, bitfieldSize),
		RecvCh:   ch,
	}, nil
}

func (mngr *PieceManager) Run(ctx context.Context, wg *sync.WaitGroup) {

	defer wg.Wait()

	for {
		select {
		case msg := <-mngr.RecvCh:
			switch msg.PayloadType {

			case messaging.BlockRequest:

				payload, ok := msg.Payload.(messaging.BlockRequestPayload)
				if !ok {
					// LOG/RETURN/WHATEVER
					return
				}

				go mngr.getBlock(payload)

			case messaging.BlockSend:

				payload, ok := msg.Payload.(messaging.BlockSendPayload)
				if !ok {
					// mngr.ErrCh <- errors.New("incorrect payload type")
					// log, whatever
					return
				}

				go mngr.setBlock(payload)
			default:
				// LOG, WHATEVER
			}
		case <-ctx.Done():
			continue
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

func (mngr *PieceManager) getBlock(data messaging.BlockRequestData) {

	mngr.mu.Lock()
	defer mngr.mu.Unlock()

	pieceIndex := data.Index
	blockOffset := data.Offset
	blockSize := data.Size
	blockIndex := blockOffset / BLOCK_SIZE

	blockData := make([]byte, blockSize)

	copy(blockData[0:], mngr.Pieces[pieceIndex].Blocks[blockIndex].Data[0:blockSize])

	mngr.SendCh <- messaging.DirectedMessage{
		MessageType: messaging.BlockSend,
		Data: messaging.BlockSendData{
			Index:  pieceIndex,
			Offset: blockOffset,
			Size:   blockSize,
			Data:   blockData,
		},
	}

	var setBits int
	for _, byte := range mngr.Bitfield {
		setBits += bits.OnesCount(uint(byte))
	}

	if setBits >= int(mngr.Metainfo.Length)/int(mngr.Metainfo.PieceLength) {
		return
	}

}

func (mngr *PieceManager) setBlock(data messaging.BlockSendData) {

	mngr.mu.Lock()
	defer mngr.mu.Unlock()

	pieceIndex := data.Index
	blockOffset := data.Offset
	blockData := data.Data
	blockIndex := blockOffset / BLOCK_SIZE

	piece := mngr.Pieces[pieceIndex]

	if piece.Blocks == nil {
		piece.Blocks = make([]Block, mngr.Metainfo.PieceLength/BLOCK_SIZE)
	}

	if piece.BlockBitfield == nil {
		piece.BlockBitfield = make(bitfield.BitfieldMask, BLOCK_BITFIELD_SIZE)
	}

	copy(piece.Blocks[blockIndex].Data[:], blockData[:])

	bitIdx := blockOffset/BLOCK_SIZE + blockOffset%BLOCK_SIZE
	piece.BlockBitfield.SetPiece(bitIdx)

	var responseBuffer []byte
	var responseType messaging.MessageType

	mngr.SendCh <- messaging.DirectedMessage{
		MessageType: responseType,
		Data:        responseBuffer,
	}

}

func (mngr *PieceManager) validatePiece(pieceData []byte, pieceIndex uint32) bool {

	mngr.mu.Lock()
	defer mngr.mu.Unlock()

	metainfoHash := [20]byte(mngr.Metainfo.Pieces[20*pieceIndex : 20*pieceIndex+20])
	recvHash := sha1.Sum(pieceData)

	return recvHash == metainfoHash
}
