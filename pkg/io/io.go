package io

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/agaabrieel/bittorrent-client/pkg/messaging"
	"github.com/agaabrieel/bittorrent-client/pkg/metainfo"
)

type IOManager struct {
	FD        *os.File
	FileSize  int64
	PieceSize int64
	SendCh    chan<- messaging.Message
	RecvCh    <-chan messaging.Message
	ErrCh     chan<- error
	Mutex     *sync.Mutex
}

func NewIOManager(meta metainfo.TorrentMetainfo, r *messaging.Router, globalCh chan<- messaging.Message, errCh chan<- error) *IOManager {

	recvCh := make(chan messaging.Message, 256)

	r.Subscribe(messaging.BlockSend, recvCh)
	r.Subscribe(messaging.PieceSend, recvCh)
	r.Subscribe(messaging.FileFinished, recvCh)

	fd, err := os.Create(meta.InfoDict.Name)
	if err != nil {
		errCh <- fmt.Errorf("file creation failed: %v", err)
		return nil
	}

	err = fd.Truncate(int64(meta.InfoDict.Length))
	if err != nil {
		errCh <- fmt.Errorf("failed to allocate enough size for the file: %v", err)
		return nil
	}

	return &IOManager{
		FD:        fd,
		PieceSize: int64(meta.InfoDict.PieceLength),
		FileSize:  int64(meta.InfoDict.Length),
		RecvCh:    recvCh,
		SendCh:    globalCh,
		ErrCh:     errCh,
		Mutex:     &sync.Mutex{},
	}
}

func (mngr *IOManager) Run(ctx context.Context, wg *sync.WaitGroup) {
	defer mngr.FD.Close()
	defer wg.Done()

	_, cancel := context.WithCancel(ctx)

	for {
		select {
		case msg := <-mngr.RecvCh:
			switch msg.MessageType {
			case messaging.BlockSend:

				payload, ok := msg.Data.(messaging.BlockSendData)
				if !ok {
					mngr.ErrCh <- errors.New("wrong data type")
					continue
				}

				offset, err := mngr.FD.Seek(int64(payload.Index)*mngr.FileSize, 0)
				if err != nil {
					mngr.ErrCh <- fmt.Errorf("file seek failed: %v", err)
					continue
				}

				wg.Add(1)
				go func() {
					defer wg.Done()

					mngr.Mutex.Lock()
					defer mngr.Mutex.Unlock()

					writtenBytes := int64(0)
					payloadSize := int64(len(payload.Data))
					for writtenBytes < payloadSize {
						n, err := mngr.FD.WriteAt(payload.Data[writtenBytes:], offset+int64(writtenBytes))
						if err != nil && err != io.EOF {
							mngr.ErrCh <- fmt.Errorf("file writing failed: %v", err)
							continue
						}
						writtenBytes += int64(n)
					}
				}()

			case messaging.PieceSend:
				// do more stuff
			case messaging.FileFinished:
				// even more stuff to do
			}
		case <-ctx.Done():
			cancel()
			return
		}
	}
}
