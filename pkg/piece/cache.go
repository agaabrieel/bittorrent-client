package piece

import (
	"container/list"
	"fmt"
	"math"
	"os"
	"sync"

	"github.com/bits-and-blooms/bitset"
)

const BLOCK_SIZE = 16 * 1024                    // 16KiB
const BLOCK_BITFIELD_SIZE = 2 * 1024            // BLOCK_SIZE / 8 = 2KiB
const PIECE_BUFFER_MAX_SIZE = 512 * 1024 * 1024 // 512 MiB

type Piece struct {
	BlockBitfield *bitset.BitSet
	Blocks        []byte
}

type PieceCache struct {
	cap         uint32
	cacheList   *list.List
	cacheMap    map[uint32]*list.Element
	filenameMap map[uint32]string
	mu          sync.RWMutex
}

type PieceCacheEntry struct {
	idx   uint32
	piece *Piece
}

func NewPieceCache(capacity uint32) *PieceCache {
	return &PieceCache{
		cap:         capacity,
		cacheList:   list.New(),
		cacheMap:    make(map[uint32]*list.Element),
		filenameMap: make(map[uint32]string),
	}
}

func (pc *PieceCache) GetPiece(idx uint32) (*Piece, error) {

	if piece, exists := pc.cacheMap[idx]; exists {
		pc.cacheList.MoveToFront(piece)
		return piece.Value.(*PieceCacheEntry).piece, nil
	}

	if filename, exists := pc.filenameMap[idx]; exists {
		f, err := os.OpenFile(filename, os.O_RDONLY, 0644)
		if err != nil {
			// log
			return nil, err
		}
		defer f.Close()

		pieceSize, err := f.Seek(-BLOCK_BITFIELD_SIZE, 2)
		if err != nil {
			// log
			return nil, err
		}

		newPiece := &Piece{
			Blocks:        make([]byte, pieceSize),
			BlockBitfield: bitset.New(BLOCK_BITFIELD_SIZE),
		}

		newPiece.BlockBitfield.ReadFrom(f)
		f.Seek(0, 0)
		f.Read(newPiece.Blocks)

		pc.putPiece(idx, newPiece)

		return newPiece, nil
	}

	return nil, fmt.Errorf("piece %d does not exist", idx)
}

func (pc *PieceCache) putPiece(idx uint32, piece *Piece) {

	// Create new entry
	newEntry := &PieceCacheEntry{
		idx:   idx,
		piece: piece,
	}

	// Push new entry to the front of the list
	elem := pc.cacheList.PushFront(newEntry)
	pc.cacheMap[idx] = elem

	// Evict oldest item if len > capacity
	if pc.cacheList.Len() > int(pc.cap) {
		lastEntry := pc.cacheList.Back()
		if lastEntry != nil {

			var err error
			var f *os.File
			// Open file if it exists or create a new tmp file
			if filename, exists := pc.filenameMap[idx]; exists {

				f, err = os.OpenFile(filename, os.O_RDWR, 0644)
				if err != nil {
					// log
					return
				}
				defer f.Close()

			} else {

				f, err = os.CreateTemp("tmp/", fmt.Sprintf("piece-%d-*", idx))
				if err != nil {
					// log
					return
				}
				defer f.Close()

				f.Truncate(int64(len(lastEntry.Value.(*PieceCacheEntry).piece.Blocks) + BLOCK_BITFIELD_SIZE))
				pc.filenameMap[idx] = f.Name()

			}

			// Serialize piece data and block bitfield data and write them to tmp file
			// first 16384 bytes correspond to a piece's block data, following 2048 bytes are the piece's block bitfield/bitset
			f.Write(lastEntry.Value.(*PieceCacheEntry).piece.Blocks)
			f.Seek(int64(len(lastEntry.Value.(*PieceCacheEntry).piece.Blocks)), 0)
			lastEntry.Value.(*PieceCacheEntry).piece.BlockBitfield.WriteTo(f)

			// Remove from list and delete from map
			pc.cacheList.Remove(lastEntry)
			delete(pc.cacheMap, lastEntry.Value.(*PieceCacheEntry).idx)
		}
	}
}

func (pc *PieceCache) GetBlock(idx, offset, size uint32) ([]byte, error) {

	piece, err := pc.GetPiece(idx)
	if err != nil {
		// log
		return nil, err
	}
	return piece.Blocks[offset : offset+size], nil
}

func (pc *PieceCache) PutBlock(data []byte, idx, offset uint32) error {

	piece, err := pc.GetPiece(idx)
	if err != nil {
		// log
		return err
	}
	copy(piece.Blocks[offset:], data)
	piece.BlockBitfield.Set(uint(BLOCK_SIZE / offset))
	pc.putPiece(idx, piece)
	return nil
}

func (pc *PieceCache) Cleanup() {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	for elem := pc.cacheList.Front(); elem != nil; elem = elem.Next() {
		elem.Value.(*PieceCacheEntry).piece = nil
	}
	pc.cacheList.Init()
	pc.cacheMap = make(map[uint32]*list.Element)
	pc.filenameMap = make(map[uint32]string)
	os.RemoveAll("tmp/")
}

func (pc *PieceCache) isPieceComplete(idx, pieceSize uint32) (bool, error) {
	piece, err := pc.GetPiece(idx)
	if err != nil {
		return false, err
	}
	totalBlocks := pieceSize/BLOCK_SIZE + uint32(math.Ceil(float64((pieceSize)%(BLOCK_SIZE))/float64(8)))

	return piece.BlockBitfield.Count() == uint(totalBlocks), nil
}
