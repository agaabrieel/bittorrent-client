package peer

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math/bits"
	"net"
	"sync"
	"time"

	bitfield "github.com/agaabrieel/bittorrent-client/pkg/bitfield"
	messaging "github.com/agaabrieel/bittorrent-client/pkg/messaging"
	"github.com/google/uuid"
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
	PeerConn        net.Conn
	PeerAddr        net.Addr
	PeerBitfield    bitfield.BitfieldMask
	IsInterested    bool
	IsInteresting   bool
	IsChoked        bool
	IsChoking       bool
	PiecesRequested []PeerMessage
	LastActive      time.Time
	LastMessage     PeerMessage
	wg              *sync.WaitGroup
	mu              *sync.RWMutex
	RecvCh          <-chan messaging.Message
	ErrorCh         chan<- error
}

func NewPeerManager(r *messaging.Router, conn net.Conn, addr net.Addr, errCh chan<- error) *PeerManager {

	id, ch := uuid.New().String(), make(chan messaging.Message, 1024)

	r.RegisterComponent(id, ch)

	return &PeerManager{
		id:       id,
		PeerConn: conn,
		PeerAddr: addr,
		mu:       &sync.RWMutex{},
		wg:       &sync.WaitGroup{},
		RecvCh:   ch,
		ErrorCh:  errCh,
	}
}

func (p *PeerManager) startPeerHandshake(ctx context.Context, infohash [20]byte, clientId [20]byte) {

	defer p.wg.Done()

	childCtx, ctxCancel := context.WithCancel(ctx)
	defer ctxCancel()

	// Dials connection
	conn, err := net.Dial("tcp", p.PeerAddr.String())
	if err != nil {
		p.ErrorCh <- fmt.Errorf("connection dialing failed: %v", err)
		return
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
		p.ErrorCh <- fmt.Errorf("incorrect handshake size: expected 68, got %d", Handshake.Len())
		return
	}

	// Sets write deadline
	if deadline, ok := childCtx.Deadline(); ok {
		conn.SetWriteDeadline(deadline)
	} else {
		conn.SetWriteDeadline(time.Now().Add(time.Second * 30))
	}

	// Writes handshake message
	writtenBytes := 0
	for writtenBytes < Handshake.Len() {
		n, err := conn.Write(Handshake.Bytes()[writtenBytes:])
		if err != nil {
			p.ErrorCh <- fmt.Errorf("failed to write to peer: %v", err)
			return
		}
		writtenBytes += n
	}

	// Sets read deadline
	if deadline, ok := childCtx.Deadline(); ok {
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
			p.ErrorCh <- fmt.Errorf("failed to write to peer: %w", err)
			return
		}
		readBytes += n
	}

	if !bytes.Equal(responseBuffer[0:28], Handshake.Bytes()[:28]) {
		p.ErrorCh <- fmt.Errorf("incorrect handshake, expected %v, got %v", responseBuffer[0:28], Handshake.Bytes()[:28])
		return
	}

	if !bytes.Equal(responseBuffer[28:48], Handshake.Bytes()[28:48]) {
		p.ErrorCh <- fmt.Errorf("infohash doesn't match, expected %v, got %v", responseBuffer[28:48], Handshake.Bytes()[28:48])
		return
	}

	peerID := *(*[20]byte)(responseBuffer[48:68])
	if p.PeerId != peerID && p.PeerId != [20]byte{} {
		p.ErrorCh <- fmt.Errorf("peer id changed, expected %v, got %v", p.PeerId, peerID)
		return
	}

	p.PeerId = peerID
	p.IsChoked = true
	p.IsInterested = false
	p.IsChoking = true
	p.IsInteresting = false
	p.LastActive = time.Now()
	p.PeerConn = conn
	p.PeerAddr = conn.RemoteAddr()
	p.LastMessage = PeerMessage{}

	p.wg.Add(1)
	go p.mainLoop(childCtx)
}

func (p *PeerManager) replyToPeerHandshake(ctx context.Context, infohash [20]byte, clientId [20]byte) {

	defer p.wg.Done()

	childCtx, ctxCancel := context.WithCancel(ctx)
	defer ctxCancel()

	if deadline, ok := childCtx.Deadline(); ok {
		p.PeerConn.SetReadDeadline(deadline)
	} else {
		p.PeerConn.SetReadDeadline(time.Now().Add(time.Second * 30))
	}

	handshakeBuffer := make([]byte, 68)
	readBytes := 0
	for readBytes < len(handshakeBuffer) {
		n, err := p.PeerConn.Read(handshakeBuffer[readBytes:])
		if err != nil {
			p.ErrorCh <- fmt.Errorf("failed to read from peer: %v", err)
			p.PeerConn.Close()
			return
		}
		readBytes += n
	}

	if len(handshakeBuffer) != 68 {
		p.ErrorCh <- fmt.Errorf("incorrect handshake size, expected 68 bytes, got %d", len(handshakeBuffer))
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
		p.ErrorCh <- fmt.Errorf("incorrect handshake")
		p.PeerConn.Close()
		return
	}

	if !bytes.Equal(handshakeBuffer[28:48], handshake.Bytes()[28:48]) {
		p.ErrorCh <- fmt.Errorf("infohash doesn't match, expected %v, got %v", handshakeBuffer[28:48], handshake.Bytes()[28:48])
		p.PeerConn.Close()
		return
	}

	peerID := *(*[20]byte)(handshakeBuffer[48:68])

	// Sets write deadline
	if deadline, ok := childCtx.Deadline(); ok {
		p.PeerConn.SetWriteDeadline(deadline)
	} else {
		p.PeerConn.SetWriteDeadline(time.Now().Add(time.Second * 30))
	}

	// Writes handshake message
	writtenBytes := 0
	for writtenBytes < handshake.Len() {
		n, err := p.PeerConn.Write(handshake.Bytes()[writtenBytes:])
		if err != nil {
			p.ErrorCh <- fmt.Errorf("failed to write to peer: %v", err)
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

	p.wg.Add(1)
	go p.mainLoop(childCtx)

}

func (p *PeerManager) mainLoop(ctx context.Context) {

	defer p.wg.Done()
	defer p.PeerConn.Close()

	childCtx, ctxCancel := context.WithCancel(ctx)
	defer ctxCancel()

	peerConnRecvCh := make(chan PeerMessage, 1024)
	peerConnSendCh := make(chan []byte, 1024)

	// Writer loop
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		go p.writeLoop(childCtx, peerConnSendCh)
	}()

	// Reader loop
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		go p.readLoop(childCtx, peerConnRecvCh)
	}()

	p.wg.Add(1)
	go func() {
		// Msg processing loop
		defer p.wg.Done()
		for {
			select {
			case <-ctx.Done():
				// log stuff
				return

			case msg := <-peerConnRecvCh:
				go func() {
					p.LastMessage = msg
					p.LastActive = time.Now()

					p.mu.Lock()
					defer p.mu.Unlock()
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
								peerConnSendCh <- generateNoPayloadMsg(Interested)
								p.IsInteresting = true
							}
						} else if p.IsInteresting {
							peerConnSendCh <- generateNoPayloadMsg(NotInterested)
							p.IsInteresting = false
						}

					case Have:
						p.PeerBitfield.SetPiece(binary.BigEndian.Uint32(msg.data[5:]))
						wantedPieces := 1

						if wantedPieces != 0 {
							if !p.IsInteresting {
								peerConnSendCh <- generateNoPayloadMsg(Interested)
								p.IsInteresting = true
							}
						} else if p.IsInteresting {
							peerConnSendCh <- generateNoPayloadMsg(NotInterested)
							p.IsInteresting = false
						}

					case Request:

					case Piece:

					}
				}()
			}
		}
	}()

	p.wg.Add(1)
	go func() {
		// Reply loop
		defer p.wg.Done()
		for {
			select {
			case <-ctx.Done():
				// LOG STUFF
				return
			case msg := <-p.RecvCh:
				go func() {
					switch msg.PayloadType {
					case messaging.BlockSend:

						payload, ok := msg.Payload.(messaging.BlockSendPayload)
						if !ok {
							p.ErrorCh <- fmt.Errorf("invalid payload type")
							return
						}
						peerConnSendCh <- payload.Data

					default:
						// LOG STUFF
						return
					}
				}()
			}
		}

	}()

}

func (p *PeerManager) readLoop(ctx context.Context, sendCh chan<- PeerMessage) {

	timer := time.NewTimer(120 * time.Second)
	defer timer.Stop()

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
					p.ErrorCh <- fmt.Errorf("reading timed-out, closing connection: %v", err)
					return
				}
				p.ErrorCh <- fmt.Errorf("read error: %v", err)
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
					p.ErrorCh <- fmt.Errorf("buffer read error: %v", err)
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
						p.ErrorCh <- fmt.Errorf("write timed-out, closing connection: %v", err)
						return
					} else if err == io.EOF {
						return
					}
					p.ErrorCh <- fmt.Errorf("write error: %v", err)
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
