package piece

import (
	"crypto/sha1"
	"encoding/binary"
	"math/bits"
	"math/rand"
	"sync"
	"time"

	bitfield "github.com/agaabrieel/bittorrent-client/pkg/bitfield"
	messaging "github.com/agaabrieel/bittorrent-client/pkg/messaging"
	"github.com/agaabrieel/bittorrent-client/pkg/metainfo"
)

const BLOCK_SIZE = 16384                   // 16KB
const BLOCK_BITFIELD_SIZE = BLOCK_SIZE / 8 // 2048

type PieceManager struct {
	Metainfo          *metainfo.TorrentMetainfoInfoDict
	Pieces            []Piece
	PeerManagerRecvCh <-chan messaging.PeerToPieceManagerMsg
	mutex             *sync.Mutex
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

func NewPieceManager(metainfo *metainfo.TorrentMetainfo, recvCh <-chan messaging.PeerToPieceManagerMsg) *PieceManager {

	fileLen := metainfo.InfoDict.Length
	pieceLen := metainfo.InfoDict.PieceLength
	// _16kb := 16383

	piecesNum := fileLen / pieceLen
	if fileLen%pieceLen != 0 {
		piecesNum++
	}

	piecesArr := make([]Piece, piecesNum)
	for _, piece := range piecesArr {
		piece.BlockBitfield = make(bitfield.BitfieldMask, BLOCK_BITFIELD_SIZE)
		piece.Blocks = make([]Block, metainfo.InfoDict.PieceLength/BLOCK_SIZE)
	}

	return &PieceManager{
		Metainfo:          metainfo.InfoDict,
		Pieces:            piecesArr,
		PeerManagerRecvCh: recvCh,
	}
}

func (mngr *PieceManager) Run() {
	for {
		select {
		case peerMsg := <-mngr.PeerManagerRecvCh:
			switch peerMsg.MessageType {
			case messaging.BlockRequest:
				go mngr.getBlock(peerMsg.ReplyCh, peerMsg.Data)
			case messaging.BlockSend:
				go mngr.setBlock(peerMsg.ReplyCh, peerMsg.Data)
			}
		case <-time.After(15 * time.Second):
			continue
		}
	}
}

func (mngr *PieceManager) pickNextPiece() {

}

func (mngr *PieceManager) pickNextBlock(pieceIndex uint32) {
	mngr.mutex.Lock()
	defer mngr.mutex.Unlock()

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
		// sendCh <- start: pieceLen * pieceIndex, offset: blockSize * (blockIndex + bitIndex)
	}

}

func (mngr *PieceManager) getBlock(replyCh chan<- messaging.PieceManagerToPeerMsg, msgData []byte) {

	mngr.mutex.Lock()
	defer mngr.mutex.Unlock()

	pieceIndex := binary.BigEndian.Uint32(msgData[0:4])
	blockOffset := binary.BigEndian.Uint32(msgData[4:8])
	blockSize := binary.BigEndian.Uint32(msgData[8:12])
	blockIndex := blockOffset / BLOCK_SIZE

	blockData := make([]byte, blockSize)

	copy(blockData[0:], mngr.Pieces[pieceIndex].Blocks[blockIndex].Data[0:blockSize])

	replyCh <- messaging.PieceManagerToPeerMsg{
		MessageType: messaging.BlockSend,
		Data:        blockData,
	}

}

func (mngr *PieceManager) setBlock(replyCh chan<- messaging.PieceManagerToPeerMsg, msgData []byte) {

	mngr.mutex.Lock()
	defer mngr.mutex.Unlock()

	pieceIndex := binary.BigEndian.Uint32(msgData[0:4])
	blockOffset := binary.BigEndian.Uint32(msgData[4:8])
	blockData := msgData[8:]
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

	if int(blockOffset)+len(blockData) == int(mngr.Metainfo.PieceLength) {

		responseBuffer = make([]byte, 4)
		binary.BigEndian.PutUint32(responseBuffer, pieceIndex)

		isPieceValid := mngr.validatePiece(blockData, pieceIndex)
		if isPieceValid {
			responseType = messaging.PieceValidated
		} else {
			responseType = messaging.PieceInvalidated
		}

	} else {
		responseBuffer = nil
		responseType = messaging.NilMsg
	}

	replyCh <- messaging.PieceManagerToPeerMsg{
		MessageType: responseType,
		Data:        responseBuffer,
	}

}

func (mngr *PieceManager) validatePiece(pieceData []byte, pieceIndex uint32) bool {

	mngr.mutex.Lock()
	defer mngr.mutex.Unlock()

	metainfoHash := [20]byte(mngr.Metainfo.Pieces[20*pieceIndex : 20*pieceIndex+20])
	recvHash := sha1.Sum(pieceData)

	return recvHash == metainfoHash
}
