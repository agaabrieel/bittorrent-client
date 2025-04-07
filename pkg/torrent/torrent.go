package session

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	messaging "github.com/agaabrieel/bittorrent-client/pkg/messaging"
	metainfo "github.com/agaabrieel/bittorrent-client/pkg/metainfo"
	peer "github.com/agaabrieel/bittorrent-client/pkg/peers"
	piece "github.com/agaabrieel/bittorrent-client/pkg/piece"
	tracker "github.com/agaabrieel/bittorrent-client/pkg/tracker"
)

type SessionManager struct {
	Id        [20]byte
	Port      int
	Metainfo  metainfo.TorrentMetainfo
	RecvCh    <-chan messaging.Message
	SendCh    chan<- messaging.Message
	WaitGroup sync.WaitGroup
	Mutex     sync.Mutex
}

func NewTorrentSession(filepath string) (*SessionManager, error) {

	meta := metainfo.NewMetainfo()
	if err := meta.Deserialize(filepath); err != nil {
		return nil, fmt.Errorf("failed to parse torrent file: %w", err)
	}

	var id [20]byte
	rand.Read(id[:])

	return &SessionManager{
		Id:        id,
		Port:      6081,
		SendCh:    make(chan messaging.Message, 256),
		RecvCh:    make(chan messaging.Message, 256),
		Mutex:     sync.Mutex{},
		WaitGroup: sync.WaitGroup{},
	}, nil

}

func (t *SessionManager) Run() {

	Router := messaging.Router{
		Subscribers: make(map[messaging.MessageType][]chan<- messaging.Message),
	}

	globalCh := make(chan messaging.Message)

	TrackerManager := tracker.NewTrackerManager(t.Metainfo.InfoDict, &Router, globalCh)

	PieceManager := piece.NewPieceManager(t.Metainfo.InfoDict, &Router, globalCh)

	PeerManager := peer.NewPeerManager(t.Metainfo.InfoDict, &Router, globalCh)

	ctx := context.Background()

	t.WaitGroup.Add(4)
	defer t.WaitGroup.Wait()

	go Router.Start(globalCh)
	go TrackerManager.Run(ctx, &t.WaitGroup)
	go PeerManager.Run(ctx, &t.WaitGroup)
	go PieceManager.Run(ctx, &t.WaitGroup)

	listener, err := net.Listen("tcp", net.JoinHostPort("localhost", strconv.Itoa(t.Port)))
	if err != nil {
		panic(err)
	}
	defer listener.Close()

	t.WaitGroup.Add(1)
	go func() {
		defer t.WaitGroup.Done()
		var conn net.Conn
		for {
			select {

			case <-ctx.Done():
				log.Default().Printf("context sent: %v", ctx.Err())
				return

			default:
				conn, err = listener.Accept()
				if err != nil {
					log.Default().Printf("connection from %v rejected: %v", conn.RemoteAddr(), err)
					conn.Close()
					continue
				}

				t.WaitGroup.Add(1)

			}
		}
	}()

	t.WaitGroup.Add(1)
	go func() {
		for {
			select {

			case <-ctx.Done():
				log.Default().Printf("context sent: %v", ctx.Err())
				return

			case a:
				// ipipipipi
			}
		}
	}()

}

func performHandshake(ctx context.Context, conn net.Conn, wg *sync.WaitGroup, sendCh chan peer.Peer, hash [20]byte, id [20]byte) {

	defer wg.Done()

	if deadline, ok := ctx.Deadline(); ok {
		conn.SetReadDeadline(deadline)
	} else {
		conn.SetReadDeadline(time.Now().Add(time.Second * 30))
	}

	handshakeBuffer := make([]byte, 68)
	readBytes := 0
	for readBytes < len(handshakeBuffer) {
		n, err := conn.Read(handshakeBuffer[readBytes:])
		if err != nil {
			log.Default().Printf("failed to read from peer: %w", err)
			conn.Close()
			continue
		}
		readBytes += n
	}

	if len(handshakeBuffer) != 68 {
		log.Default().Printf("incorrect handshake size, expected 68 bytes, got %d", len(handshakeBuffer))
		conn.Close()
		return
	}

	// Generates handshake msg
	var handshake bytes.Buffer
	handshake.WriteByte(0x13)
	handshake.WriteString("BitTorrent protocol")
	handshake.Write(make([]byte, 8))
	handshake.Write(hash[:])
	handshake.Write(id[:])

	if !bytes.Equal(handshakeBuffer[0:28], handshake.Bytes()[:28]) {
		log.Default().Printf("peer sent incorrect handshake")
		conn.Close()
		return
	}

	if !bytes.Equal(handshakeBuffer[28:48], handshake.Bytes()[28:48]) {
		log.Default().Printf("info hash sent by peer doesn't match ours")
		conn.Close()
		return
	}

	peerID := *(*[20]byte)(handshakeBuffer[48:68])

	// Sets write deadline
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetWriteDeadline(deadline)
	} else {
		conn.SetWriteDeadline(time.Now().Add(time.Second * 30))
	}

	// Writes handshake message
	writtenBytes := 0
	for writtenBytes < handshake.Len() {
		n, err := conn.Write(handshake.Bytes()[writtenBytes:])
		if err != nil {
			log.Default().Printf("failed to write to peer: %w", err)
			conn.Close()
			return
		}
		writtenBytes += n
	}

	sendCh <- peer.Peer{
		Id:            peerID,
		IsChoked:      true,
		IsChoking:     true,
		IsInterested:  false,
		IsInteresting: false,
		LastActive:    time.Now(),
		LastMessage:   peer.PeerMessage{},
		Conn:          conn,
	}
}
