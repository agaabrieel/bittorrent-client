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
	metainfo "github.com/agaabrieel/bittorrent-client/pkg/metainfo"
	piece "github.com/agaabrieel/bittorrent-client/pkg/piece"
)

const PEER_WORKERS = 10

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
	KeepAlive         // NOT PROTOCOL-COMPLIANT
	HandshakeResponse // NOT PROTOCOL-COMPLIANT
)

type PeerMessage struct {
	data        []byte
	messageType PeerMessageType
}

type PeerManager struct {
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
	Mutex           *sync.Mutex
	OrchestratorSendCh chan int
	OrchestratorRecvCh chan int
}

type PeerConnection struct {
	net.Conn
}

type PeerOrchestrator struct {
	clientId       [20]byte
	clientInfohash [20]byte
	AskedBlocks    []piece.Block
	Bitfield       bitfield.BitfieldMask
	Peers          []PeerManager
	Mutex          sync.RWMutex
	Waitgroup      sync.WaitGroup
	SendCh         chan<- messaging.Message
	RecvCh         <-chan messaging.Message
	PeerManagerRecvCh  <-chan messaging.Message
	PeerManagerSendChMap map[[20]byte]chan<- messaging.Message
	ErrorCh        chan<- error
}

func NewPeerOrchestrator(meta metainfo.TorrentMetainfo, r *messaging.Router, globalCh chan messaging.Message, id [20]byte) *PeerOrchestrator {

	recvCh := make(chan messaging.Message, 256)

	r.Subscribe(messaging.NewPeerFromTracker, recvCh)
	r.Subscribe(messaging.PeerUpdate, recvCh)
	r.Subscribe(messaging.BlockSend, recvCh)
	r.Subscribe(messaging.PieceInvalidated, recvCh)
	r.Subscribe(messaging.PieceValidated, recvCh)
	r.Subscribe(messaging.FileFinished, recvCh)

	return &PeerOrchestrator{
		Peers:          make([]PeerManager, 100),
		clientId:       id,
		clientInfohash: meta.Infohash,
		Bitfield:       bitfield.NewBitfield(uint32(float64((meta.InfoDict.Length / meta.InfoDict.PieceLength) / 8))),
		ErrorCh:        make(chan error, 256),
		RecvCh:         recvCh,
		SendCh:         globalCh,
		Mutex:          sync.RWMutex{},
		Waitgroup:      sync.WaitGroup{},
	}
}

func (mngr *PeerOrchestrator) Run(ctx context.Context, wg *sync.WaitGroup) {

	defer wg.Done()
	childCtx, ctxCancel := context.WithCancel(ctx)

	for {
		select {
		case msg := <-mngr.RecvCh:
			switch msg.MessageType {
			case messaging.NewPeerFromTracker, messaging.NewPeerConnection:

				peerMngr, ok := msg.Data.(PeerManager)
				if !ok {
					mngr.ErrorCh <- errors.New("payload has incorrect type")
					ctxCancel()
				}

				wg.Add(1)
				if msg.MessageType == messaging.NewPeerFromTracker {
					peerMngr.WaitGroup.Add(1)
					go peerMngr.startPeerHandshake(childCtx, mngr.ErrorCh, mngr.clientInfohash, mngr.clientId)
				} else {
					peerMngr.WaitGroup.Add(1)
					go peerMngr.replyToPeerHandshake(childCtx, mngr.ErrorCh, mngr.clientInfohash, mngr.clientId)
				}

			}
		case msg := <-mngr.PeerManagerCh:
			switch msg.MessageType {

			}
		case <-ctx.Done():
			ctxCancel()
			return // should add logging here
		}
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
	peerMsgCh := make(chan PeerMessage, 1024)

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
				p.LastMessage = msg
				p.LastActive = time.Now()
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
					wantedPieces := p.PeerBitfield.

					if len(wantedPieces) != 0 {
						if !p.IsInteresting {
							p.IsInteresting = true
						}
					} else if p.IsInteresting {
						p.IsInteresting = false
					}

				case Have:
					p.PeerBitfield.SetPiece(binary.BigEndian.Uint32(msg.data[5:]))
					wantedPieces := mngr.getWantedPieces(p)

					if wantedPieces != nil {
						if !p.IsInteresting {
							peerSendCh <- mngr.generateNoPayloadMsg(Interested)
							p.IsInteresting = true
						}
					} else if p.IsInteresting {
						peerSendCh <- mngr.generateNoPayloadMsg(NotInterested)
						p.IsInteresting = false
					}

				// Most actions will happen as responses to these 2 message types, as the default case or after the timer
				case Request:

					if p.IsChoked {
						return
					}

					if binary.BigEndian.Uint32(msg.data[13:17]) > piece.BLOCK_SIZE {
						return
						//errCh <- fmt.Errorf("peer request has size size %d, expected 16 Kb or smaller", requestedPieceLen)
					}

					msgData := make([]byte, binary.BigEndian.Uint32(msg.data[0:4])-1)
					copy(msgData[:], msg.data[5:])

					mngr.SendCh <- messaging.Message{
						MessageType: messaging.BlockRequest,
						Data:        msgData,
					}

					reply := <-mngr.RecvCh

					payload, ok := reply.Data.(messaging.BlockSendData)
					if !ok {
						mngr.ErrorCh <- errors.New("wrong payload type")
						continue
					}

					peerSendCh <- payload.Data

				case Piece:

					msgData := make([]byte, binary.BigEndian.Uint32(msg.data[0:4])-1) // -1 to account for message ID
					copy(msgData[:], msg.data[5:])

					mngr.SendCh <- messaging.Message{
						MessageType: messaging.BlockSend,
						Data:        msgData,
					}

					reply := <-mngr.RecvCh

					pieceIdx := uint32(6)

					mngr.Mutex.Lock()
					if reply.MessageType == messaging.PieceValidated {
						mngr.Bitfield.SetPiece(pieceIdx)
						peerSendCh <- mngr.generateHaveMsg(pieceIdx)
					} else if reply.MessageType == messaging.PieceInvalidated {
						mngr.Bitfield.ZeroPiece(pieceIdx)
					}
					mngr.Mutex.Unlock()
				}
			}
		}
	}()

	p.WaitGroup.Add(1)
	go func() {
		// Reply loop
		defer p.WaitGroup.Done()
	
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

func (mngr *PeerOrchestrator) generateNoPayloadMsg(msgType PeerMessageType) []byte {

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

func (mngr *PeerOrchestrator) generateHaveMsg(idx uint32) []byte {
	buf := make([]byte, 9) // 4 bytes for length prefix, 1 byte for message id and 4 bytes for piece index
	binary.BigEndian.PutUint32(buf, 5)
	buf[4] = byte(Have)
	binary.BigEndian.PutUint32(buf[5:], idx)
	return buf
}

func (mngr *PeerOrchestrator) generateRequestMsg(idx uint32, offset uint32, len uint32) []byte {
	buf := make([]byte, 17)
	binary.BigEndian.PutUint32(buf, 13)
	buf[4] = byte(Request)
	binary.BigEndian.PutUint32(buf[5:9], idx)
	binary.BigEndian.PutUint32(buf[9:13], offset)
	binary.BigEndian.PutUint32(buf[13:17], len)
	return buf
}

func (mngr *PeerOrchestrator) generateBitfieldMsg() []byte {
	buf := make([]byte, len(mngr.Bitfield)+5)
	binary.BigEndian.PutUint32(buf, uint32(len(mngr.Bitfield)+1))
	buf[4] = byte(Bitfield)
	copy(buf[5:], mngr.Bitfield)
	return buf
}

func (mngr *PeerOrchestrator) generatePieceMsg(idx uint32, offset uint32, data []byte) []byte {
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
