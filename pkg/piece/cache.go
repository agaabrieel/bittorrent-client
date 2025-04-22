package piece

import (
	"container/list"
	"fmt"
	"os"
	"sync"

	bitfield "github.com/agaabrieel/bittorrent-client/pkg/bitfield"
)

const BLOCK_SIZE = 16 * 1024                    // 16KiB
const BLOCK_BITFIELD_SIZE = 2 * 1024            // BLOCK_SIZE / 8 = 2KiB
const PIECE_BUFFER_MAX_SIZE = 512 * 1024 * 1024 // 512 MiB

type Piece struct {
	BlockBitfield bitfield.BitfieldMask
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

	var newPiece *Piece
	if filename, exists := pc.filenameMap[idx]; exists {
		data, err := os.ReadFile(filename)
		if err != nil {
			// log
			return nil, err
		}

		newPiece = &Piece{
			Blocks:        data[:BLOCK_SIZE],
			BlockBitfield: bitfield.BitfieldMask(data[BLOCK_SIZE : BLOCK_SIZE+BLOCK_BITFIELD_SIZE]),
		}

	} else {
		newPiece = &Piece{}
	}

	pc.putPiece(idx, newPiece)
	return newPiece, nil
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
				defer f.Close()
				if err != nil {
					// log
					return
				}
			} else {
				f, err = os.CreateTemp("tmp/", fmt.Sprintf("piece-%d-*", idx))
				defer f.Close()
				if err != nil {
					// log
					return
				}
				pc.filenameMap[idx] = f.Name()
			}

			// Serialize piece data and block bitfield data and write them to tmp file
			data := make([]byte, BLOCK_SIZE+BLOCK_BITFIELD_SIZE)
			copy(data[:BLOCK_SIZE], lastEntry.Value.(*PieceCacheEntry).piece.Blocks)
			copy(data[BLOCK_SIZE:], lastEntry.Value.(*PieceCacheEntry).piece.BlockBitfield) // first 16384 bytes correspond to a piece's block data, following 2048 bytes are the piece's block bitfield/bitset

			f.Write(data)

			// Remove from list and delete from map
			pc.cacheList.Remove(lastEntry)
			delete(pc.cacheMap, lastEntry.Value.(*PieceCacheEntry).idx)
		}
	}
}

func (pc *PieceCache) GetBlock(idx, offset uint32) ([]byte, error) {

	piece, err := pc.GetPiece(idx)
	if err != nil {
		// log
		return nil, err
	}
	return piece.Blocks[offset:], nil
}

func (pc *PieceCache) PutBlock(data []byte, idx, offset uint32) error {

	piece, err := pc.GetPiece(idx)
	if err != nil {
		// log
		return err
	}
	copy(piece.Blocks[offset:], data)
	piece.BlockBitfield.SetPiece(BLOCK_SIZE / offset)
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
