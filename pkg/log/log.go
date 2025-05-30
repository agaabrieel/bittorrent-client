package log

import (
	"context"
	"log"
	"os"
	"sync"

	"github.com/agaabrieel/bittorrent-client/pkg/messaging"
	"github.com/agaabrieel/bittorrent-client/pkg/metainfo"
)

type Logger struct {
	id     string
	recvCh <-chan messaging.Message
	*log.Logger
}

func NewLogger(meta *metainfo.TorrentMetainfo, r *messaging.Router, clientId [20]byte) (*Logger, error) {

	id, ch := "logger", make(chan messaging.Message, 1024)
	err := r.RegisterComponent(id, ch)
	if err != nil {
		return nil, err
	}

	f, err := os.Create("log.txt")
	if err != nil {
		return nil, err
	}

	logger := log.New(f, "", 10111)

	return &Logger{
		id:     id,
		recvCh: ch,
		Logger: logger,
	}, nil
}

func (l *Logger) Run(ctx context.Context, wg *sync.WaitGroup) {

	defer wg.Done()

	for {
		select {
		case msg := <-l.recvCh:
			l.Printf("Message of type %v sent by %s at %v, expected reply to %s. Payload is: %+v", msg.PayloadType, msg.SourceId, msg.CreatedAt, msg.ReplyTo, msg.Payload)
		case <-ctx.Done():
			return
		}
	}
}
