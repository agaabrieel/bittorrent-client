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
	"github.com/google/uuid"
)

const WORKER_POOL_SIZE int = 10

type IOManager struct {
	id           string
	file         *os.File
	Router       *messaging.Router
	FileSize     int64
	PieceSize    int64
	SentMessages map[string]bool
	RecvCh       <-chan messaging.Message
	mu           sync.RWMutex
	wg           sync.WaitGroup
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

	defer func() {
		if err != nil {
			f.Close()
		}
	}()

	err = f.Truncate(int64(meta.InfoDict.Length))
	if err != nil {
		return nil, fmt.Errorf("failed to allocate enough size for the file: %v", err)
	}

	return &IOManager{
		id:           id,
		file:         f,
		Router:       r,
		PieceSize:    int64(meta.InfoDict.PieceLength),
		FileSize:     int64(meta.InfoDict.Length),
		RecvCh:       ch,
		SentMessages: make(map[string]bool),
		mu:           sync.RWMutex{},
		wg:           sync.WaitGroup{},
	}, nil
}

func (mngr *IOManager) Start(ctx context.Context, wg *sync.WaitGroup) {
	defer mngr.file.Close()
	defer wg.Done()

	_, cancel := context.WithCancel(ctx)
	defer cancel()

	workersJobsCh := make(chan messaging.Message, 1)
	for range WORKER_POOL_SIZE {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for msg := range workersJobsCh {

				if msg.ReplyingTo != "" {
					mngr.mu.Lock()
					exists := mngr.SentMessages[msg.ReplyingTo]
					if !exists {
						mngr.Router.Send(msg.ReplyTo, messaging.Message{
							Id:          uuid.NewString(),
							SourceId:    mngr.id,
							ReplyingTo:  msg.Id,
							PayloadType: messaging.Error,
							Payload: messaging.ErrorPayload{
								Message:     fmt.Sprintf("unexpected message with payload %v replying to %s", msg.PayloadType, msg.ReplyingTo),
								ErrorCode:   messaging.ErrCodeInvalidPayload,
								Severity:    messaging.Warning,
								Time:        time.Now(),
								ComponentId: mngr.id,
							},
							CreatedAt: time.Now(),
						})
						return
					}
					delete(mngr.SentMessages, msg.ReplyingTo)
					mngr.mu.Unlock()
				}

				switch msg.PayloadType {
				case messaging.PieceSend:

					payload, ok := msg.Payload.(messaging.PieceSendPayload)
					if !ok {
						mngr.Router.Send(msg.ReplyTo, messaging.Message{
							Id:          uuid.NewString(),
							SourceId:    mngr.id,
							ReplyingTo:  msg.Id,
							PayloadType: messaging.Error,
							Payload: messaging.ErrorPayload{
								Message:   "type validation failed",
								ErrorCode: messaging.ErrCodeInvalidPayload,
								Severity:  messaging.Warning,
							},
							CreatedAt: time.Now(),
						})
						return
					}

					mngr.mu.Lock()
					defer mngr.mu.Unlock()

					offset, err := mngr.file.Seek(int64(payload.Index)*mngr.PieceSize, 0)
					if err != nil {

						mngr.Router.Send(msg.ReplyTo, messaging.Message{
							Id:          uuid.NewString(),
							SourceId:    mngr.id,
							ReplyingTo:  msg.Id,
							PayloadType: messaging.Error,
							Payload: messaging.ErrorPayload{
								Message:     fmt.Sprintf("file seek failed: %s", err.Error()),
								ErrorCode:   messaging.ErrCodeInternalError,
								Severity:    messaging.Warning,
								ComponentId: "io_manager",
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

							mngr.Router.Send(msg.ReplyTo, messaging.Message{
								SourceId:    mngr.id,
								ReplyingTo:  msg.Id,
								PayloadType: messaging.Error,
								Payload: messaging.ErrorPayload{
									Message:     fmt.Sprintf("file writing failed: %v", err),
									Severity:    messaging.Warning,
									ErrorCode:   messaging.ErrCodeInternalError,
									ComponentId: "io_manager",
								},
								CreatedAt: time.Now(),
							})
							return
						}
						writtenBytes += int64(n)
					}

					if msg.ReplyTo != "" {
						mngr.Router.Send(msg.ReplyTo, messaging.Message{
							Id:          uuid.NewString(),
							SourceId:    mngr.id,
							ReplyingTo:  msg.Id,
							PayloadType: messaging.Acknowledged,
							Payload:     nil,
							CreatedAt:   time.Now(),
						})
					}

				case messaging.BlockRequest:

					mngr.mu.RLock()
					defer mngr.mu.RUnlock()

					payload, ok := msg.Payload.(messaging.BlockRequestPayload)
					if !ok {
						mngr.Router.Send(msg.ReplyTo, messaging.Message{
							Id:          uuid.NewString(),
							SourceId:    mngr.id,
							ReplyingTo:  msg.Id,
							PayloadType: messaging.Error,
							Payload: messaging.ErrorPayload{
								Message:   "type validation failed",
								ErrorCode: messaging.ErrCodeInvalidPayload,
								Severity:  messaging.Warning,
							},
							CreatedAt: time.Now(),
						})
						return
					}

					buffer := make([]byte, payload.Size)

					offset, err := mngr.file.Seek(int64(payload.Index)*mngr.PieceSize+int64(payload.Offset), 0)
					if err != nil {
						mngr.Router.Send(msg.ReplyTo, messaging.Message{
							Id:          uuid.NewString(),
							SourceId:    mngr.id,
							ReplyingTo:  msg.Id,
							PayloadType: messaging.Error,
							Payload: messaging.ErrorPayload{
								Message:     fmt.Sprintf("file seek failed: %s", err.Error()),
								ErrorCode:   messaging.ErrCodeInternalError,
								Severity:    messaging.Warning,
								ComponentId: "io_manager",
							},
							CreatedAt: time.Now(),
						})
						return
					}

					writtenBytes := int64(0)
					for writtenBytes < int64(payload.Size) {
						n, err := mngr.file.ReadAt(buffer[writtenBytes:], offset+int64(writtenBytes))
						if err != nil && err != io.EOF {

							mngr.Router.Send(msg.ReplyTo, messaging.Message{
								SourceId:    mngr.id,
								ReplyingTo:  msg.Id,
								PayloadType: messaging.Error,
								Payload: messaging.ErrorPayload{
									Message:     fmt.Sprintf("file reading failed: %v", err),
									Severity:    messaging.Warning,
									ErrorCode:   messaging.ErrCodeInternalError,
									ComponentId: "io_manager",
								},
								CreatedAt: time.Now(),
							})
							return

						}
						writtenBytes += int64(n)
					}

					if msg.ReplyTo == "" {
						mngr.Router.Send("error_handler", messaging.Message{
							Id:          uuid.NewString(),
							SourceId:    mngr.id,
							ReplyingTo:  msg.Id,
							PayloadType: messaging.Error,
							Payload: messaging.ErrorPayload{
								Message:     "message payload requires reply but doesn't contain a ReplyTo id",
								Severity:    messaging.Warning,
								ErrorCode:   messaging.ErrCodeInternalError,
								ComponentId: "io_manager",
							},
							CreatedAt: time.Now(),
						})
					} else {
						mngr.Router.Send(msg.ReplyTo, messaging.Message{
							Id:          uuid.NewString(),
							SourceId:    mngr.id,
							ReplyTo:     mngr.id,
							ReplyingTo:  msg.Id,
							PayloadType: messaging.BlockSend,
							Payload: messaging.BlockSendPayload{
								Index:  payload.Index,
								Offset: payload.Offset,
								Size:   payload.Size,
								Data:   buffer,
							},
							CreatedAt: time.Now(),
						})
					}
				}
			}
		}()
	}

	for {
		select {
		case msg := <-mngr.RecvCh:
			workersJobsCh <- msg
		case <-ctx.Done():
			return
		}
	}
}
