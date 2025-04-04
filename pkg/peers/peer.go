package peer

import (
	"bytes"
	"context"
	"encoding/binary"
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

type Peer struct {
	Id              [20]byte
	Ip              net.IP
	Port            uint16
	Conn            net.Conn
	Bitfield        bitfield.BitfieldMask
	IsInterested    bool
	IsInteresting   bool
	IsChoked        bool
	IsChoking       bool
	PiecesRequested []PeerMessage
	LastActive      time.Time
	LastMessage     PeerMessage
	mu              *sync.RWMutex
}

type PeerManager struct {
	id                 [20]byte
	AskedBlocks        []piece.Block
	Bitfield           bitfield.BitfieldMask
	Peers              []Peer
	Metainfo           *metainfo.TorrentMetainfo
	mutex              *sync.RWMutex
	waitgroup          *sync.WaitGroup
	SessionSendCh      chan messaging.ChannelMessage
	SessionRecvCh      chan messaging.ChannelMessage
	PieceManagerSendCh chan<- messaging.PeerToPieceManagerMsg
	ErrorCh            chan error
}

func NewPeerManager(peers []Peer, bitfield bitfield.BitfieldMask, trackerCh <-chan []Peer, errCh chan error, sendCh chan<- messaging.PeerToPieceManagerMsg, metainfo *metainfo.TorrentMetainfo) *PeerManager {

	//bitfieldSize := metainfo.InfoDict.Length / 8
	//if metainfo.InfoDict.Length%8 != 0 {
	//	bitfieldSize++
	//}

	mngr := PeerManager{
		Peers:                peers,
		Metainfo:             metainfo,
		Bitfield:             bitfield,
		PieceManagerSendCh:   sendCh,
		TrackerManagerRecvCh: trackerCh,
		ErrorCh:              errCh,
		mutex:                &sync.RWMutex{},
		waitgroup:            &sync.WaitGroup{},
	}
	return &mngr
}

func (mngr *PeerManager) Run(ctx context.Context) {

	defer mngr.waitgroup.Wait()

	if ctx == nil {
		ctx = context.Background()
	}

	for _, peer := range mngr.Peers {
		mngr.waitgroup.Add(1)
		go mngr.handlePeer(&peer, ctx)
	}

	for {
		select {}
	}
}

func (mngr *PeerManager) handlePeer(p *Peer, ctx context.Context) {

	defer mngr.waitgroup.Done()
	defer p.Conn.Close()

	if ctx == nil {
		ctx = context.Background()
	}

	// Resolves address
	addr, err := net.ResolveTCPAddr("tcp", net.JoinHostPort(p.Ip.String(), strconv.Itoa(int(p.Port))))
	if err != nil {
		mngr.ErrorCh <- fmt.Errorf("failed to resolve tcp address from peer: %w", err)
	}

	err = doHandshake(p, ctx, *addr, mngr.Metainfo.Infohash, mngr.id)
	if err != nil {
		mngr.ErrorCh <- fmt.Errorf("failed to establish handshake with peer %x: %w", p.Id[:], err)
	}

	pieceManagerRecvCh := make(chan messaging.PieceManagerToPeerMsg)

	recvCh := make(chan PeerMessage, 1024)
	sendCh := make(chan []byte, 1024)

	// Writer loop
	mngr.waitgroup.Add(1)
	go func() {
		defer mngr.waitgroup.Done()
		go writeLoop(p, ctx, sendCh)
	}()

	// Reader loop
	mngr.waitgroup.Add(1)
	go func() {
		defer mngr.waitgroup.Done()
		go readLoop(p, ctx, recvCh)
	}()

	for {
		select {
		case <-ctx.Done():
			return

		case msg := <-recvCh:
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
				p.Bitfield = msg.data
				wantedPieces := mngr.getWantedPieces(p)

				if wantedPieces != nil {
					if !p.IsInteresting {
						sendCh <- mngr.generateNoPayloadMsg(Interested)
						p.IsInteresting = true
					}
				} else if p.IsInteresting {
					sendCh <- mngr.generateNoPayloadMsg(NotInterested)
					p.IsInteresting = false
				}

			case Have:
				p.Bitfield.SetPiece(binary.BigEndian.Uint32(msg.data[5:]))
				wantedPieces := mngr.getWantedPieces(p)

				if wantedPieces != nil {
					if !p.IsInteresting {
						sendCh <- mngr.generateNoPayloadMsg(Interested)
						p.IsInteresting = true
					}
				} else if p.IsInteresting {
					sendCh <- mngr.generateNoPayloadMsg(NotInterested)
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

				mngr.PieceManagerSendCh <- messaging.PeerToPieceManagerMsg{
					MessageType: messaging.BlockRequest,
					Data:        msgData,
					ReplyCh:     pieceManagerRecvCh,
				}

				reply := <-pieceManagerRecvCh

				if reply.MessageType == messaging.BlockSend {
					sendCh <- reply.Data
				}

			case Piece:

				msgData := make([]byte, binary.BigEndian.Uint32(msg.data[0:4])-1) // -1 to account for message ID
				copy(msgData[:], msg.data[5:])

				mngr.PieceManagerSendCh <- messaging.PeerToPieceManagerMsg{
					MessageType: messaging.BlockSend,
					Data:        msgData,
					ReplyCh:     pieceManagerRecvCh,
				}

				reply := <-pieceManagerRecvCh
				pieceIdx := binary.BigEndian.Uint32(reply.Data[0:4])

				mngr.mutex.Lock()
				if reply.MessageType == messaging.PieceValidated {
					mngr.Bitfield.SetPiece(pieceIdx)
					sendCh <- mngr.generateHaveMsg(pieceIdx)
				} else if reply.MessageType == messaging.PieceInvalidated {
					mngr.Bitfield.ZeroPiece(pieceIdx)
				}
				mngr.mutex.Unlock()
			}

		default:
			if p.IsInteresting && !p.IsChoking {
				sendCh <- mngr.generateRequestMsg()
			}
		}
	}
}

func doHandshake(p *Peer, ctx context.Context, addr net.TCPAddr, hash [20]byte, id [20]byte) error {

	// Dials connection
	conn, err := net.Dial("tcp", addr.String())
	if err != nil {
		return fmt.Errorf("failed to connect to peer: %w", err)
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
	Handshake.Write(hash[:])
	Handshake.Write(id[:])

	if Handshake.Len() != 68 {
		return fmt.Errorf("incorrect handshake size: expected 68, got %d", Handshake.Len())
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
			return fmt.Errorf("failed to write to peer: %w", err)
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
			return fmt.Errorf("failed to write to peer: %w", err)
		}
		readBytes += n
	}

	if !bytes.Equal(responseBuffer[0:28], Handshake.Bytes()[:28]) {
		return fmt.Errorf("peer sent incorrect handshake")
	}

	if !bytes.Equal(responseBuffer[28:48], Handshake.Bytes()[28:48]) {
		return fmt.Errorf("info hash sent by peer doesn't match ours")
	}

	peerID := *(*[20]byte)(responseBuffer[48:68])
	if p.Id != peerID && p.Id != [20]byte{} {
		return fmt.Errorf("peer ID changed: was %x, now %x", p.Id, peerID)
	}

	p.Id = peerID
	p.IsChoked = true
	p.IsInterested = false
	p.IsChoking = true
	p.IsInteresting = false
	p.LastActive = time.Now()
	p.Conn = conn
	p.LastMessage = PeerMessage{
		messageType: HandshakeResponse,
		data:        nil,
	}

	return nil
}

func readLoop(p *Peer, ctx context.Context, recvCh chan<- PeerMessage) {

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
			p.Conn.SetReadDeadline(time.Now().Add(time.Second * 30))

			n, err := p.Conn.Read(buf)
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
					recvCh <- PeerMessage{
						data:        fullMsg,
						messageType: KeepAlive,
					}
				} else {
					recvCh <- PeerMessage{
						data:        fullMsg,
						messageType: PeerMessageType(fullMsg[4]),
					}
				}
			}
		}
	}
}

func writeLoop(p *Peer, ctx context.Context, sendCh <-chan []byte) {

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
		case msg := <-sendCh:

			totalLen := len(msg)

			bytesWritten := 0
			for bytesWritten < totalLen {
				p.Conn.SetWriteDeadline(time.Now().Add(time.Second * 15))

				n, err := p.Conn.Write(msg[bytesWritten:])
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

func (mngr *PeerManager) generateNoPayloadMsg(msgType PeerMessageType) []byte {

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

func (mngr *PeerManager) generateHaveMsg(idx uint32) []byte {
	buf := make([]byte, 9) // 4 bytes for length prefix, 1 byte for message id and 4 bytes for piece index
	binary.BigEndian.PutUint32(buf, 5)
	buf[4] = byte(Have)
	binary.BigEndian.PutUint32(buf[5:], idx)
	return buf
}

func (mngr *PeerManager) generateRequestMsg(idx uint32, offset uint32, len uint32) []byte {
	buf := make([]byte, 17)
	binary.BigEndian.PutUint32(buf, 13)
	buf[4] = byte(Request)
	binary.BigEndian.PutUint32(buf[5:9], idx)
	binary.BigEndian.PutUint32(buf[9:13], offset)
	binary.BigEndian.PutUint32(buf[13:17], len)
	return buf
}

func (mngr *PeerManager) generateBitfieldMsg() []byte {
	buf := make([]byte, len(mngr.Bitfield)+5)
	binary.BigEndian.PutUint32(buf, uint32(len(mngr.Bitfield)+1))
	buf[4] = byte(Bitfield)
	copy(buf[5:], mngr.Bitfield)
	return buf
}

func (mngr *PeerManager) generatePieceMsg(idx uint32, offset uint32, data []byte) []byte {
	buf := make([]byte, len(data)+13)
	binary.BigEndian.PutUint32(buf, uint32(len(data)+9))
	buf[4] = byte(Piece)
	copy(buf[5:], data)
	return buf
}

func (mngr *PeerManager) getWantedPieces(p *Peer) bitfield.BitfieldMask {

	mngr.mutex.Lock()
	defer mngr.mutex.Unlock()

	var WantedPieces bitfield.BitfieldMask
	for i, _ := range mngr.Bitfield {
		WantedPieces[i] = ((mngr.Bitfield[i] ^ p.Bitfield[i]) & p.Bitfield[i])
	}

	return WantedPieces
	// Xor gets pieces that the client or the peer have, but not both
	// And gets pieces the the client has, i.e, that we don't but they have

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
