package peer

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	metainfo "github.com/agaabrieel/bittorrent-client/pkg/metainfo"
	piece "github.com/agaabrieel/bittorrent-client/pkg/piece"
)

type PeerMessage struct {
	data        []byte
	messageType PeerMessageType
}

type PeerMessageType uint8

const (
	Choke         PeerMessageType = 0x00
	Unchoke       PeerMessageType = 0x01
	Interested    PeerMessageType = 0x02
	NotInterested PeerMessageType = 0x03
	Have          PeerMessageType = 0x04
	Bitfield      PeerMessageType = 0x05
	Request       PeerMessageType = 0x06
	Piece         PeerMessageType = 0x07
	Cancel        PeerMessageType = 0x08
	KeepAlive     PeerMessageType = 0x09 // NOT PROTOCOL-COMPLIANT
)

type PeerManager struct {
	WantedPieces       BitfieldMask
	Bitfield           BitfieldMask
	Peers              []Peer
	PieceManagerSendCh chan piece.Piece
	PieceManagerRecvCh chan piece.Piece
	ErrorSendCh        chan error
	Metainfo           *metainfo.TorrentMetainfo
	mutex              *sync.Mutex
}

type Peer struct {
	Id         [20]byte
	Ip         net.IP
	Port       uint16
	Conn       net.Conn
	Bitfield   BitfieldMask
	Interested bool
	Choke      bool
	LastActive  time.Time
	LastMessage PeerMessage
}

func NewPeerManager(peers []Peer, pieceManager *piece.PieceManager, metainfo *metainfo.TorrentMetainfo) *PeerManager {
	mngr := PeerManager{
		Peers:        peers,
		PieceManager: pieceManager,
		Metainfo:     metainfo,
	}
	return &mngr
}

func (mngr *PeerManager) Init(ctx context.Context) {
	var wg sync.WaitGroup

	if ctx == nil {
		ctx = context.Background()
	}

	for _, peer := range mngr.Peers {
		wg.Add(1)
		go mngr.handlePeer(&wg, &peer, ctx, mngr.Metainfo.Infohash, mngr.Metainfo.Infohash)
	}
	wg.Wait()
}

func (mngr *PeerManager) handlePeer(wg *sync.WaitGroup, p *Peer, ctx context.Context, hash [20]byte, id [20]byte) error {

	defer wg.Done()

	if ctx == nil {
		ctx = context.Background()
	}

	// Resolves address
	addr, err := net.ResolveTCPAddr("tcp", net.JoinHostPort(p.Ip.String(), strconv.Itoa(int(p.Port))))
	if err != nil {
		return fmt.Errorf("failed to resolve tcp address from peer: %w", err)
	}

	conn, err := doHandshake(p, ctx, *addr, hash, id)
	if err != nil {
		return fmt.Errorf("failed to establish handshake with peer %x: %w", p.Id[:], err)
	}

	recvCh := make(chan PeerMessage, 1024)
	sendCh := make(chan []byte, 1024)

	go writeLoop(ctx, conn, sendCh)
	go readLoop(ctx, conn, recvCh)

	for {
		select {
		case msg := <-recvCh:
			p.Conn.LastMessage = msg
			switch msg.messageType {
			case Choke:
				p.Choke = true
				break
			case Unchoke:
				p.Choke = false
				break
			case NotInterested:
				p.Interested = false
				break
			case Interested:
				p.Interested = true
				break
			case Bitfield:
				p.Bitfield = msg.data
				mngr.setWantedPieces(p)

			case Have:
				mngr.evaluatePeerBitfield(Bitfield)
				sendCh <- Interested
			case Request:

			case Piece:
			}
		case <-time.After(time.Second * 120):
			sendCh <- mngr.gen
		}

		if p.Choke {
			continue
		} else if {

		}
	}
	return nil
}

func doHandshake(p *Peer, ctx context.Context, addr net.TCPAddr, hash [20]byte, id [20]byte) (net.Conn, error) {
	// Dials connection
	conn, err := net.Dial("tcp", addr.String())
	if err != nil {
		return nil, fmt.Errorf("failed to connect to peer: %w", err)
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
		err = fmt.Errorf("incorrect handshake size: expected 68, got %d", Handshake.Len())
		return nil, err
	}

	// Sets write deadline
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetWriteDeadline(deadline)
	} else {
		conn.SetWriteDeadline(time.Now().Add(time.Second * 30))
	}

	// Writes handshake message
	_, err = conn.Write(Handshake.Bytes())
	if err != nil {
		return nil, fmt.Errorf("failed to write to peer: %w", err)
	}
	// Sets read deadline
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetReadDeadline(deadline)
	} else {
		conn.SetReadDeadline(time.Now().Add(time.Second * 30))
	}

	// zeroes buffer and reads handshake response
	responseBuffer := make([]byte, 68)
	if _, err := io.ReadFull(conn, responseBuffer); err != nil {
		return nil, fmt.Errorf("couldn't read handshake: %w", err)
	}

	if !bytes.Equal(responseBuffer[0:28], Handshake.Bytes()[:28]) {
		return nil, fmt.Errorf("peer sent incorrect handshake")
	}

	if !bytes.Equal(responseBuffer[28:48], Handshake.Bytes()[28:48]) {
		return nil, fmt.Errorf("info hash sent by peer doesn't match ours")
	}

	peerID := *(*[20]byte)(responseBuffer[48:68])
	if p.Id != peerID && p.Id != [20]byte{} {
		return nil, fmt.Errorf("peer ID changed: was %x, now %x", p.Id, peerID)
	}

	p.Id = peerID
	p.Conn = PeerConn{
		Conn:       conn,
		LastActive: time.Now(),
	}

	return conn, nil
}

func readLoop(p *Peer, ctx context.Context, recvCh chan<- PeerMessage) {

	if ctx == nil {
		ctx = context.Background()
	}
	defer p.Conn.Close()

	var (
		readBuf bytes.Buffer
		buf     = make([]byte, 4096)
	)

	for {
		select {
		case <-ctx.Done():
			return

		default:
			p.Conn.SetReadDeadline(time.Now().Add(time.Second * 15))

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

				recvCh <- PeerMessage{
					data:        fullMsg,
					messageType: PeerMessageType(fullMsg[4]),
				}
			}
		}
	}
}

func writeLoop(p *Peer, ctx context.Context, sendCh <-chan []byte) {

	if ctx == nil {
		ctx = context.Background()
	}

	defer p.Conn.Close()

	for {
		select {
		case <-ctx.Done():
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

func (mngr *PeerManager) generateMsg(msg PeerMessage) ([]byte, error) {

	switch msg.messageType {
	case KeepAlive:
		buf := make([]byte, 4)
		binary.BigEndian.PutUint32(buf, 0)
		return buf, nil

	case Choke:
	case Unchoke:
	case Interested:
	case NotInterested:
		buf := make([]byte, 5) // 4 bytes for length prefix + 1 byte for message id
		binary.BigEndian.PutUint32(buf, 1)
		buf[4] = byte(msg.messageType)
		return buf, nil

	case Have:
		if len(msg.data) != 4 {
			return nil, fmt.Errorf("malformed message, expected 4 bytes, got %d", len(msg.data))
		}

		buf := make([]byte, 9) // 4 bytes for length prefix, 1 byte for message id and 4 bytes for piece index
		binary.BigEndian.PutUint32(buf, 5)
		buf[4] = byte(msg.messageType)
		copy(buf[5:], msg.data)
		return buf, nil

	case Request:
	case Cancel:
		buf := make([]byte, 17)
		binary.BigEndian.PutUint32(buf, 13)
		buf[4] = byte(msg.messageType)
		copy(buf[5:], msg.data)
		return buf, nil

	case Bitfield:
		buf := make([]byte, len(msg.data)+5)
		binary.BigEndian.PutUint32(buf, uint32(len(msg.data)+1))
		buf[4] = byte(msg.messageType)
		copy(buf[5:], msg.data)
		return buf, nil

	case Piece:
		buf := make([]byte, len(msg.data)+13)
		binary.BigEndian.PutUint32(buf, uint32(len(msg.data)+9))
		buf[4] = byte(msg.messageType)
		copy(buf[5:], msg.data)
		return buf, nil
	}
	return nil, fmt.Errorf("message type %v is invalid", msg.messageType)
}

func (mngr *PeerManager) setWantedPieces(p *Peer) {

	mngr.mutex.Lock()
	defer mngr.mutex.Unlock()
	for i, _ := range mngr.WantedPieces {
		mngr.WantedPieces[i] = ((mngr.Bitfield[i] ^ p.Bitfield[i]) & p.Bitfield[i])
	}

	// Xor gets pieces that the client or the peer have, but not both
	// And gets pieces the the client has, i.e, that we don't but they have

}
