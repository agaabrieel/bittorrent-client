package io

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/agaabrieel/bittorrent-client/pkg/messaging"
	"github.com/agaabrieel/bittorrent-client/pkg/metainfo"
)

type IOManager struct {
	id        string
	file      *os.File
	Router    *messaging.Router
	FileSize  int64
	PieceSize int64
	RecvCh    <-chan messaging.Message
	mu        *sync.RWMutex
	wg        *sync.WaitGroup
}

func NewIOManager(meta *metainfo.TorrentMetainfo, r *messaging.Router) (*IOManager, error) {

	id, ch := "io_manager", make(chan messaging.Message, 1024)
	err := r.RegisterComponent(id, ch)
	if err != nil {
		return nil, fmt.Errorf("failed to register component: %v", err)
	}

	f, err := os.Create(meta.InfoDict.Name)
	if err != nil {
		return nil, fmt.Errorf("file creation failed: %v", err)
	}

	err = f.Truncate(int64(meta.InfoDict.Length))
	if err != nil {
		return nil, fmt.Errorf("failed to allocate enough size for the file: %v", err)
	}

	return &IOManager{
		id:        id,
		file:      f,
		Router:    r,
		PieceSize: int64(meta.InfoDict.PieceLength),
		FileSize:  int64(meta.InfoDict.Length),
		RecvCh:    ch,
		mu:        &sync.RWMutex{},
		wg:        &sync.WaitGroup{},
	}, nil
}

func (mngr *IOManager) Run(ctx context.Context, wg *sync.WaitGroup) {
	defer mngr.file.Close()
	defer wg.Done()

	_, cancel := context.WithCancel(ctx)

	for {
		select {
		case msg := <-mngr.RecvCh:
			switch msg.PayloadType {
			case messaging.PieceSend:

				wg.Add(1)
				go func() {

					defer wg.Done()

					mngr.mu.Lock()
					defer mngr.mu.Unlock()

					payload, ok := msg.Payload.(messaging.PieceSendPayload)
					if !ok {
						mngr.Router.Send("", messaging.Message{
							SourceId:    mngr.id,
							PayloadType: messaging.Error,
							Payload: messaging.ErrorPayload{
								Msg: "type validation failed",
							},
							CreatedAt: time.Now(),
						})
						return
					}

					offset, err := mngr.file.Seek(int64(payload.Index)*mngr.FileSize, 0)
					if err != nil {

						mngr.Router.Send("", messaging.Message{
							SourceId:    mngr.id,
							PayloadType: messaging.Error,
							Payload: messaging.ErrorPayload{
								Msg: fmt.Sprintf("file seek failed: %v", err),
							},
							CreatedAt: time.Now(),
						})
						return
					}

					writtenBytes := int64(0)
					payloadSize := int64(len(payload.Data))
					for writtenBytes < payloadSize {

						n, err := mngr.file.WriteAt(payload.Data[writtenBytes:], offset+int64(writtenBytes))

						if err != nil && err != io.EOF {

							mngr.Router.Send("", messaging.Message{
								SourceId:    mngr.id,
								PayloadType: messaging.Error,
								Payload: messaging.ErrorPayload{
									Msg: fmt.Sprintf("file writing failed: %v", err),
								},
								CreatedAt: time.Now(),
							})
							return
						}
						writtenBytes += int64(n)
					}
				}()

			case messaging.BlockRequest:

				wg.Add(1)
				go func() {

					defer wg.Done()

					mngr.mu.RLock()
					defer mngr.mu.RUnlock()

					payload, ok := msg.Payload.(messaging.BlockRequestPayload)
					if !ok {
						mngr.Router.Send("", messaging.Message{
							SourceId:    mngr.id,
							PayloadType: messaging.Error,
							Payload: messaging.ErrorPayload{
								Msg: "type validation failed",
							},
							CreatedAt: time.Now(),
						})
						return
					}

					buffer := make([]byte, payload.Size)

					offset, err := mngr.file.Seek(int64(payload.Index)*mngr.FileSize+int64(payload.Offset), 0)
					if err != nil {
						mngr.Router.Send("", messaging.Message{
							SourceId:    mngr.id,
							PayloadType: messaging.Error,
							Payload: messaging.ErrorPayload{
								Msg: fmt.Sprintf("file seek failed: %v", err),
							},
							CreatedAt: time.Now(),
						})
						return
					}

					writtenBytes := int64(0)
					for writtenBytes < int64(payload.Size) {
						n, err := mngr.file.ReadAt(buffer[writtenBytes:], offset+int64(writtenBytes))
						if err != nil && err != io.EOF {

							mngr.Router.Send("", messaging.Message{
								SourceId:    mngr.id,
								PayloadType: messaging.Error,
								Payload: messaging.ErrorPayload{
									Msg: fmt.Sprintf("file write failed: %v", err),
								},
								CreatedAt: time.Now(),
							})
							return

						}
						writtenBytes += int64(n)
					}

					mngr.Router.Send(msg.ReplyTo, messaging.Message{
						SourceId:    mngr.id,
						ReplyTo:     mngr.id,
						PayloadType: messaging.BlockSend,
						Payload: messaging.BlockSendPayload{
							Index:  payload.Index,
							Offset: payload.Offset,
							Size:   payload.Size,
							Data:   buffer,
						},
						CreatedAt: time.Now(),
					})

				}()
			}
		case <-ctx.Done():
			cancel()
			return
		}
	}
}
