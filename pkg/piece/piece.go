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
	id                  string
	ClientId            [20]byte
	FileCache           *FileCache
	PieceFileMap        map[uint32]string
	PieceBlocksBitfield map[uint32][]bitfield.BitfieldMask
	Metainfo            *metainfo.TorrentMetainfoInfoDict
	Bitfield            bitfield.BitfieldMask
	RecvCh              <-chan messaging.Message
	mu                  *sync.RWMutex
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
	pieceFileMap := make(map[uint32]string, int(math.Ceil(float64(meta.InfoDict.Length)/float64(meta.InfoDict.PieceLength))))
	pieceBlocksBitfieldMap := make(map[uint32][]bitfield.BitfieldMask, int(meta.InfoDict.Length))

	return &PieceManager{
		id:                  id,
		ClientId:            clientId,
		PieceFileMap:        pieceFileMap,
		FileCache:           NewFileCache(MAX_OPEN_FILES),
		PieceBlocksBitfield: pieceBlocksBitfieldMap,
		Metainfo:            meta.InfoDict,
		Bitfield:            make(bitfield.BitfieldMask, bitfieldSize),
		RecvCh:              ch,
		mu:                  &sync.RWMutex{},
	}, nil
}

func (mngr *PieceManager) Run(ctx context.Context, wg *sync.WaitGroup) {

	defer wg.Wait()

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

				go mngr.getBlock(msg)

			case messaging.BlockSend:

				_, ok := msg.Payload.(messaging.BlockSendPayload)
				if !ok {
					// mngr.ErrCh <- errors.New("incorrect payload type")
					// log, whatever
					return
				}

				go mngr.setBlock(msg)
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

func (mngr *PieceManager) getBlock(msg messaging.Message) {

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

func (mngr *PieceManager) setBlock(msg messaging.Message) {

	mngr.mu.Lock()
	defer mngr.mu.Unlock()

	data, ok := msg.Payload.(messaging.BlockSendPayload)
	if !ok {
		return
	}

	blockOffset := data.Offset
	blockIdx := data.Index
	blockData := data.Data

	f, err := mngr.FileCache.GetFile(blockIdx)
	if err != nil {
		// log shit
		return
	}

	if mngr.PieceBlocksBitfield[blockIdx] == nil {
		mngr.PieceBlocksBitfield[blockIdx] = make([]bitfield.BitfieldMask, (mngr.Metainfo.PieceLength/BLOCK_BITFIELD_SIZE)/8)
	}
	bf := mngr.PieceBlocksBitfield[blockIdx]

	bf[blockIdx/8].SetPiece(uint32(blockIdx % 8))

	f.WriteAt(blockData, int64(blockIdx)*int64(mngr.Metainfo.PieceLength)+int64(blockOffset))

}

func (mngr *PieceManager) validatePiece(pieceData []byte, pieceIndex uint32) bool {

	mngr.mu.RLock()
	defer mngr.mu.RUnlock()

	metainfoHash := [20]byte(mngr.Metainfo.Pieces[20*pieceIndex : 20*pieceIndex+20])
	recvHash := sha1.Sum(pieceData)

	return recvHash == metainfoHash
}
