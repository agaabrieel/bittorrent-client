package piece

import (
	"sync"
	"time"

	bitfield "github.com/agaabrieel/bittorrent-client/pkg/bitfield"
	"github.com/agaabrieel/bittorrent-client/pkg/metainfo"
)

const BLOCK_SIZE = 16384                   // 16KB
const BLOCK_BITFIELD_SIZE = BLOCK_SIZE / 8 // 2048

type PieceManagerMessageType uint8

const (
	PieceFinished PieceManagerMessageType = iota
	FileFinished
)

type PieceManagerMessage struct {
	MessageType     PieceManagerMessageType
	FinishedMessage PieceFinishedMessage
	// More messages...
}

type PieceFinishedMessage struct {
	PieceIndex uint32
}

type PieceManager struct {
	Metainfo          *metainfo.TorrentMetainfoInfoDict
	Pieces            []Piece
	PeerManagerSendCh chan<- PieceManagerMessage
	PeerManagerRecvCh <-chan Block
	Mutex             *sync.Mutex
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
}

type Piece struct {
	BlockBitfield bitfield.BitfieldMask
	Blocks        []Block
}

func NewPieceManager(metainfo *metainfo.TorrentMetainfo, sendCh chan<- PieceManagerMessage, recvCh <-chan Block) *PieceManager {

	fileLen := metainfo.InfoDict.Length
	pieceLen := metainfo.InfoDict.PieceLength
	// _16kb := 16383

	piecesNum := fileLen / pieceLen
	if fileLen%pieceLen != 0 {
		piecesNum++
	}

	return &PieceManager{
		Metainfo:          metainfo.InfoDict,
		Pieces:            make([]Piece, piecesNum),
		PeerManagerSendCh: sendCh,
		PeerManagerRecvCh: recvCh,
	}
}

func (mngr *PieceManager) Run() {
	for {
		select {
		case newBlock := <-mngr.PeerManagerRecvCh:
			go mngr.setBlock(&newBlock)
		case <-time.After(15 * time.Second):
			continue
		}
	}
}

func (mngr *PieceManager) setBlock(block *Block) {

	mngr.Mutex.Lock()
	defer mngr.Mutex.Unlock()

	piece := mngr.Pieces[block.Index]

	if piece.Blocks == nil {
		piece.Blocks = make([]Block, mngr.Metainfo.PieceLength/BLOCK_SIZE)
	}

	blockIdx := block.Offset / BLOCK_SIZE
	copy(piece.Blocks[blockIdx].Data[:], block.Data[:])

	if int(block.Offset)+len(block.Data) == int(mngr.Metainfo.PieceLength) {

		if piece.BlockBitfield == nil {
			piece.BlockBitfield = make(bitfield.BitfieldMask, BLOCK_BITFIELD_SIZE)
		}

		piece.BlockBitfield.SetPiece(blockIdx)

		mngr.PeerManagerSendCh <- PieceManagerMessage{
			MessageType: PieceFinished,
			FinishedMessage: PieceFinishedMessage{
				PieceIndex: block.Index,
			},
		}
	}

}

func (mngr *PieceManager) validatePiece() {
	// do stuff
}
