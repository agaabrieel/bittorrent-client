package piece

import (
	"container/list"
	"os"
	"sync"
)

const MAX_OPEN_FILES = 100

type CacheEntry struct {
	idx uint32
	fd  *os.File
}

type FileCache struct {
	maxFiles  uint16
	cacheMap  map[int]*list.Element
	cacheList *list.List
	mu        sync.Mutex
}

func NewFileCache(maxFiles int) *FileCache {
	return &FileCache{
		maxFiles:  uint16(maxFiles),
		cacheMap:  make(map[int]*list.Element),
		cacheList: list.New(),
	}
}

func (fc *FileCache) GetFile(blockIdx uint32) (*os.File, error) {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	if f, exists := fc.cacheMap[int(blockIdx)]; exists {
		fc.cacheList.MoveToFront(f)
		return f.Value.(*CacheEntry).fd, nil
	}

	f, err := os.CreateTemp("tmp/", "piece-*")
	if err != nil {
		// log err
		return nil, err
	}

	entry := &CacheEntry{
		idx: blockIdx,
		fd:  f,
	}
	elem := fc.cacheList.PushFront(entry)
	fc.cacheMap[int(blockIdx)] = elem

	if fc.cacheList.Len() > int(fc.maxFiles) {
		oldest := fc.cacheList.Back()
		oldestEntry := oldest.Value.(*CacheEntry)
		oldestEntry.fd.Close()
		delete(fc.cacheMap, int(oldestEntry.idx))
		fc.cacheList.Remove(oldest)
	}

	return f, nil

}

func (fc *FileCache) Close() {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	for elem := fc.cacheList.Front(); elem != nil; elem = elem.Next() {
		entry := elem.Value.(*CacheEntry)
		entry.fd.Close()
	}
	fc.cacheMap = make(map[int]*list.Element)
	fc.cacheList.Init()
}
