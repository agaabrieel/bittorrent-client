package peer

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"math/bits"
	"net"
	"strconv"
	"sync"
	"time"

	bitfield "github.com/agaabrieel/bittorrent-client/pkg/bitfield"
	messaging "github.com/agaabrieel/bittorrent-client/pkg/messaging"
)

type PeerMessageType uint8

const (
	Choke PeerMessageType = iota
	Unchoke
	Interested
	NotInterested
	Have
	Bitfield
	Request
	Piece
	Cancel
	Port              // DHT
	KeepAlive         // NOT PROTOCOL-COMPLIANT, FOR INTERNAL USE ONLY
	HandshakeResponse // NOT PROTOCOL-COMPLIANT, FOR INTERNAL USE ONLY
)

type PeerMessage struct {
	data        []byte
	messageType PeerMessageType
}

type PeerManager struct {
	id              string
	Router          *messaging.Router
	PeerId          [20]byte
	PeerIp          net.IP
	PeerPort        uint16
	PeerConn        net.Conn
	PeerBitfield    bitfield.BitfieldMask
	IsInterested    bool
	IsInteresting   bool
	IsChoked        bool
	IsChoking       bool
	PiecesRequested []PeerMessage
	LastActive      time.Time
	LastMessage     PeerMessage
	WaitGroup       *sync.WaitGroup
	Mutex           *sync.RWMutex
	RecvCh          <-chan messaging.DirectedMessage
	ErrCh           chan<- error
}

func NewPeerManager(r *messaging.Router, conn net.Conn) *PeerManager {

	id, ch := r.NewComponent()

	return &PeerManager{
		id:        id,
		PeerConn:  conn,
		Mutex:     &sync.RWMutex{},
		WaitGroup: &sync.WaitGroup{},
		RecvCh:    ch,
	}
}

func (p *PeerManager) startPeerHandshake(ctx context.Context, errCh chan<- error, infohash [20]byte, clientId [20]byte) {

	defer p.WaitGroup.Done()

	// Dials connection
	conn, err := net.Dial("tcp", p.PeerIp.String()+strconv.Itoa(int(p.PeerPort)))
	if err != nil {
		errCh <- fmt.Errorf("failed to connect to peer: %w", err)
	}

	defer func() {
		if err != nil {
			conn.Close()
		}
	}()

	// Generates handshake msg
	var Handshake bytes.Buffer
	Handshake.WriteByte(0x13)
	Handshake.WriteString("BitTorrent protocol")
	Handshake.Write(make([]byte, 8))
	Handshake.Write(infohash[:])
	Handshake.Write(clientId[:])

	if Handshake.Len() != 68 {
		errCh <- fmt.Errorf("incorrect handshake size: expected 68, got %d", Handshake.Len())
	}

	// Sets write deadline
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetWriteDeadline(deadline)
	} else {
		conn.SetWriteDeadline(time.Now().Add(time.Second * 30))
	}

	// Writes handshake message
	writtenBytes := 0
	for writtenBytes < Handshake.Len() {
		n, err := conn.Write(Handshake.Bytes()[writtenBytes:])
		if err != nil {
			errCh <- fmt.Errorf("failed to write to peer: %w", err)
		}
		writtenBytes += n
	}

	// Sets read deadline
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetReadDeadline(deadline)
	} else {
		conn.SetReadDeadline(time.Now().Add(time.Second * 30))
	}

	// reads handshake response
	responseBuffer := make([]byte, 68)
	readBytes := 0
	for readBytes < len(responseBuffer) {
		n, err := conn.Read(responseBuffer[readBytes:])
		if err != nil {
			errCh <- fmt.Errorf("failed to write to peer: %w", err)
		}
		readBytes += n
	}

	if !bytes.Equal(responseBuffer[0:28], Handshake.Bytes()[:28]) {
		errCh <- fmt.Errorf("peer sent incorrect handshake")
	}

	if !bytes.Equal(responseBuffer[28:48], Handshake.Bytes()[28:48]) {
		errCh <- fmt.Errorf("info hash sent by peer doesn't match ours")
	}

	peerID := *(*[20]byte)(responseBuffer[48:68])
	if p.PeerId != peerID && p.PeerId != [20]byte{} {
		errCh <- fmt.Errorf("peer ID changed: was %x, now %x", p.PeerId, peerID)
	}

	p.PeerId = peerID
	p.IsChoked = true
	p.IsInterested = false
	p.IsChoking = true
	p.IsInteresting = false
	p.LastActive = time.Now()
	p.PeerConn = conn
	p.LastMessage = PeerMessage{
		messageType: HandshakeResponse,
		data:        nil,
	}

	p.WaitGroup.Add(1)
	go p.mainLoop(ctx)
}

func (p *PeerManager) replyToPeerHandshake(ctx context.Context, errCh chan<- error, infohash [20]byte, clientId [20]byte) {

	defer p.WaitGroup.Done()

	if deadline, ok := ctx.Deadline(); ok {
		p.PeerConn.SetReadDeadline(deadline)
	} else {
		p.PeerConn.SetReadDeadline(time.Now().Add(time.Second * 30))
	}

	handshakeBuffer := make([]byte, 68)
	readBytes := 0
	for readBytes < len(handshakeBuffer) {
		n, err := p.PeerConn.Read(handshakeBuffer[readBytes:])
		if err != nil {
			errCh <- fmt.Errorf("failed to read from peer: %v", err)
			p.PeerConn.Close()
			return
		}
		readBytes += n
	}

	if len(handshakeBuffer) != 68 {
		errCh <- fmt.Errorf("incorrect handshake size, expected 68 bytes, got %d", len(handshakeBuffer))
		p.PeerConn.Close()
		return
	}

	// Generates handshake msg
	var handshake bytes.Buffer
	handshake.WriteByte(0x13)
	handshake.WriteString("BitTorrent protocol")
	handshake.Write(make([]byte, 8))
	handshake.Write(infohash[:])
	handshake.Write(clientId[:])

	if !bytes.Equal(handshakeBuffer[0:28], handshake.Bytes()[:28]) {
		errCh <- fmt.Errorf("peer sent incorrect handshake")
		p.PeerConn.Close()
		return
	}

	if !bytes.Equal(handshakeBuffer[28:48], handshake.Bytes()[28:48]) {
		errCh <- fmt.Errorf("info hash sent by peer doesn't match ours")
		p.PeerConn.Close()
		return
	}

	peerID := *(*[20]byte)(handshakeBuffer[48:68])

	// Sets write deadline
	if deadline, ok := ctx.Deadline(); ok {
		p.PeerConn.SetWriteDeadline(deadline)
	} else {
		p.PeerConn.SetWriteDeadline(time.Now().Add(time.Second * 30))
	}

	// Writes handshake message
	writtenBytes := 0
	for writtenBytes < handshake.Len() {
		n, err := p.PeerConn.Write(handshake.Bytes()[writtenBytes:])
		if err != nil {
			errCh <- fmt.Errorf("failed to write to peer: %v", err)
			p.PeerConn.Close()
			return
		}
		writtenBytes += n
	}

	p.PeerId = peerID
	p.IsChoked = true
	p.IsChoking = true
	p.IsInterested = false
	p.IsInteresting = false
	p.LastActive = time.Now()
	p.LastMessage = PeerMessage{}

	p.WaitGroup.Add(1)
	go p.mainLoop(ctx)

}

func (p *PeerManager) mainLoop(ctx context.Context) {

	defer p.WaitGroup.Done()
	defer p.PeerConn.Close()

	peerRecvCh := make(chan PeerMessage, 1024)
	peerSendCh := make(chan []byte, 1024)

	// Writer loop
	p.WaitGroup.Add(1)
	go func() {
		defer p.WaitGroup.Done()
		go p.writeLoop(ctx, peerSendCh)
	}()

	// Reader loop
	p.WaitGroup.Add(1)
	go func() {
		defer p.WaitGroup.Done()
		go p.readLoop(ctx, peerRecvCh)
	}()

	p.WaitGroup.Add(1)
	go func() {
		// Msg processing loop
		defer p.WaitGroup.Done()
		for {
			select {
			case <-ctx.Done():
				return

			case msg := <-peerRecvCh:
				go func() {
					p.LastMessage = msg
					p.LastActive = time.Now()

					p.Mutex.Lock()
					defer p.Mutex.Unlock()
					switch msg.messageType {

					// First 6 cases simply update the internal peer status
					case Choke:
						p.IsChoking = true

					case Unchoke:
						p.IsChoking = false

					case NotInterested:
						p.IsInterested = false

					case Interested:
						p.IsInterested = true

					case Bitfield:
						p.PeerBitfield = msg.data
						wantedPieces := p.PeerBitfield

						if len(wantedPieces) != 0 {
							if !p.IsInteresting {
								peerSendCh <- generateNoPayloadMsg(Interested)
								p.IsInteresting = true
							}
						} else if p.IsInteresting {
							peerSendCh <- generateNoPayloadMsg(NotInterested)
							p.IsInteresting = false
						}

					case Have:
						p.PeerBitfield.SetPiece(binary.BigEndian.Uint32(msg.data[5:]))
						wantedPieces := 1

						if wantedPieces != 0 {
							if !p.IsInteresting {
								peerSendCh <- generateNoPayloadMsg(Interested)
								p.IsInteresting = true
							}
						} else if p.IsInteresting {
							peerSendCh <- generateNoPayloadMsg(NotInterested)
							p.IsInteresting = false
						}

					case Request:

					case Piece:

					}
				}()
			}
		}
	}()

	p.WaitGroup.Add(1)
	go func() {
		// Reply loop
		defer p.WaitGroup.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-p.RecvCh:
				go func() {
					switch msg.MessageType {
					case messaging.BlockSend:

						data, ok := msg.Data.(messaging.BlockSendData)
						if !ok {
							p.SendCh <- messaging.DirectedMessage{
								MessageType: messaging.Error,
								Data:        errors.New("invalid payload type"),
							}
							return
						}

						peerSendCh <- data.Data

					}
				}()
			}
		}

	}()

}

func (p *PeerManager) readLoop(ctx context.Context, sendCh chan<- PeerMessage) {

	timer := time.NewTimer(120 * time.Second)
	defer timer.Stop()

	if ctx == nil {
		ctx = context.Background()
	}

	var (
		readBuf bytes.Buffer
		buf     = make([]byte, 4096)
	)

	for {
		timer.Reset(120 * time.Second)
		select {
		case <-ctx.Done():
			return

		case <-timer.C:
			return

		default:
			p.PeerConn.SetReadDeadline(time.Now().Add(time.Second * 30))

			n, err := p.PeerConn.Read(buf)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					log.Printf("reading timed-out, closing connection: %v", err)
					return
				} else if err == io.EOF {
					return
				}
				log.Printf("read error: %v", err)
				return
			}

			readBuf.Write(buf[:n])

			for {
				if readBuf.Len() < 4 {
					break
				}

				msgLen := binary.BigEndian.Uint32(readBuf.Bytes()[:4])
				if readBuf.Len() < int(msgLen) {
					break
				}

				fullMsg := make([]byte, msgLen)
				_, err := readBuf.Read(fullMsg)
				if err != nil {
					log.Printf("buffer read error: %v", err)
				}

				if msgLen == 4 {
					sendCh <- PeerMessage{
						data:        fullMsg,
						messageType: KeepAlive,
					}
				} else {
					sendCh <- PeerMessage{
						data:        fullMsg,
						messageType: PeerMessageType(fullMsg[4]),
					}
				}
			}
		}
	}
}

func (p *PeerManager) writeLoop(ctx context.Context, recvCh <-chan []byte) {

	timer := time.NewTimer(120 * time.Second)
	defer timer.Stop()

	if ctx == nil {
		ctx = context.Background()
	}

	for {
		timer.Reset(120 * time.Second)
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			return
		case msg := <-recvCh:

			totalLen := len(msg)

			bytesWritten := 0
			for bytesWritten < totalLen {
				p.PeerConn.SetWriteDeadline(time.Now().Add(time.Second * 15))

				n, err := p.PeerConn.Write(msg[bytesWritten:])
				bytesWritten += n
				if err != nil {
					if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
						log.Printf("write timed-out, closing connection: %v", err)
						return
					} else if err == io.EOF {
						return
					}
					log.Printf("write error: %v", err)
					return
				}
			}
		}
	}
}

func generateNoPayloadMsg(msgType PeerMessageType) []byte {

	switch msgType {
	case KeepAlive:
		buf := make([]byte, 4)
		binary.BigEndian.PutUint32(buf, 0)
		return buf

	default:
		buf := make([]byte, 5) // 4 bytes for length prefix + 1 byte for message id
		binary.BigEndian.PutUint32(buf, 1)
		buf[4] = byte(msgType)
		return buf
	}
}

func generateHaveMsg(idx uint32) []byte {
	buf := make([]byte, 9) // 4 bytes for length prefix, 1 byte for message id and 4 bytes for piece index
	binary.BigEndian.PutUint32(buf, 5)
	buf[4] = byte(Have)
	binary.BigEndian.PutUint32(buf[5:], idx)
	return buf
}

func generateRequestMsg(idx uint32, offset uint32, len uint32) []byte {
	buf := make([]byte, 17)
	binary.BigEndian.PutUint32(buf, 13)
	buf[4] = byte(Request)
	binary.BigEndian.PutUint32(buf[5:9], idx)
	binary.BigEndian.PutUint32(buf[9:13], offset)
	binary.BigEndian.PutUint32(buf[13:17], len)
	return buf
}

func generateBitfieldMsg(bf bitfield.BitfieldMask) []byte {
	buf := make([]byte, len(bf)+5)
	binary.BigEndian.PutUint32(buf, uint32(len(bf)+1))
	buf[4] = byte(Bitfield)
	copy(buf[5:], bf)
	return buf
}

func generatePieceMsg(idx uint32, offset uint32, data []byte) []byte {
	buf := make([]byte, len(data)+13)
	binary.BigEndian.PutUint32(buf, uint32(len(data)+9))
	buf[4] = byte(Piece)
	copy(buf[5:], data)
	return buf
}

func findFirstNonZeroBit(bytes []byte) int {
	for i, byte := range bytes {
		if byte == 0 {
			continue
		}
		bitIdx := bits.Len(uint(byte)) - 1
		return i + bitIdx
	}
	return -1
}
