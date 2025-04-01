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
	metainfo "github.com/agaabrieel/bittorrent-client/pkg/metainfo"
	piece "github.com/agaabrieel/bittorrent-client/pkg/piece"
)

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
	Port          PeerMessageType = 0x09 // DHT
	KeepAlive     PeerMessageType = 0x10 // NOT PROTOCOL-COMPLIANT
)

type PeerMessage struct {
	data        []byte
	messageType PeerMessageType
}

type Peer struct {
	Id            [20]byte
	Ip            net.IP
	Port          uint16
	Conn          net.Conn
	Bitfield      bitfield.BitfieldMask
	IsInterested  bool
	IsInteresting bool
	IsChoked      bool
	IsChoking     bool
	LastActive    time.Time
	LastMessage   PeerMessage
}

type PeerManager struct {
	id                   [20]byte
	Bitfield             bitfield.BitfieldMask
	Peers                []Peer
	TrackerManagerRecvCh <-chan []Peer
	PieceManagerSendCh   chan<- piece.Block
	PieceManagerRecvCh   <-chan piece.PieceManagerMessage
	ErrorCh              chan error
	Metainfo             *metainfo.TorrentMetainfo
	mutex                *sync.Mutex
	waitgroup            *sync.WaitGroup
}

func NewPeerManager(peers []Peer, bitfield bitfield.BitfieldMask, trackerCh <-chan []Peer, errCh chan error, sendCh chan<- piece.Block, recvCh <-chan piece.PieceManagerMessage, metainfo *metainfo.TorrentMetainfo) *PeerManager {

	//bitfieldSize := metainfo.InfoDict.Length / 8
	//if metainfo.InfoDict.Length%8 != 0 {
	//	bitfieldSize++
	//}

	mngr := PeerManager{
		Peers:                peers,
		Metainfo:             metainfo,
		Bitfield:             bitfield,
		PieceManagerSendCh:   sendCh,
		PieceManagerRecvCh:   recvCh,
		TrackerManagerRecvCh: trackerCh,
		ErrorCh:              errCh,
		mutex:                &sync.Mutex{},
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
		go mngr.handlePeer(&peer, ctx, mngr.Metainfo.Infohash, mngr.Metainfo.Infohash)
	}

	for {
		select {
		case trackerManagerMsg := <-mngr.TrackerManagerRecvCh:
			// do stuff
		case pieceManagerMsg := <-mngr.PieceManagerRecvCh:
			if pieceManagerMsg.MessageType == piece.PieceFinished {
				mngr.mutex.Lock()
				mngr.Bitfield.SetPiece(pieceManagerMsg.FinishedMessage.PieceIndex)
				mngr.mutex.Unlock()
			}
		case errorMsg := <-mngr.ErrorCh:
			if errorMsg == "end" {
				return
			}
		case <-ctx.Done():
			return ctx.Err().Error()
		}
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

	recvCh := make(chan PeerMessage, 1024)
	sendCh := make(chan []byte, 1024)

	mngr.waitgroup.Add(2)
	go writeLoop(mngr.waitgroup, p, ctx, sendCh)
	go readLoop(mngr.waitgroup, p, ctx, recvCh)

	for {
		select {
		case msg := <-recvCh:
			p.LastMessage = msg
			switch msg.messageType {

			case Choke:

				p.IsChoking = true
				break

			case Unchoke:

				p.IsChoking = false
				wantedPieces := mngr.getWantedPieces(p)
				idx := findFirstNonZeroBit(wantedPieces)
				var resp []byte

				if idx != -1 {
					resp = mngr.generateRequestMsg()
					sendCh <- resp
				}
				break

			case NotInterested:

				p.IsInterested = false
				break

			case Interested:

				var resp []byte
				p.IsInterested = true
				if p.IsChoked {
					resp = mngr.generateNoPayloadMsg(Unchoke)
					sendCh <- resp
				}
				break

			case Bitfield:

				p.Bitfield = msg.data
				wantedPieces := mngr.getWantedPieces(p)
				idx := findFirstNonZeroBit(wantedPieces)
				var resp []byte

				if idx != -1 {
					if p.IsChoking {
						resp = mngr.generateNoPayloadMsg(Interested)
					} else {
						resp = mngr.generateRequestMsg()
					}
				} else {
					resp = mngr.generateNoPayloadMsg(NotInterested)
				}
				sendCh <- resp
				break

			case Have:

				pieceIdx := binary.BigEndian.Uint32(msg.data[5:])
				p.Bitfield.SetPiece(pieceIdx)

				var resp []byte
				mngr.mutex.Lock()
				if !mngr.Bitfield.HasPiece(pieceIdx) {
					mngr.mutex.Unlock()

					if !p.IsInteresting {
						p.IsInteresting = true
					}

					if p.IsChoking {
						resp = mngr.generateNoPayloadMsg(Interested)
					} else {
						resp = mngr.generateRequestMsg()
					}
				} else {
					resp = mngr.generateNoPayloadMsg(NotInterested)
				}
				sendCh <- resp

			case Request:

				requestedPieceLen := binary.BigEndian.Uint32(msg.data[13:17])

				if requestedPieceLen > piece.BLOCK_SIZE {
					//errCh <- fmt.Errorf("peer request has size size %d, expected 16 Kb or smaller", requestedPieceLen)
				}

				requestedPieceIdx := binary.BigEndian.Uint32(msg.data[5:9])
				requestedPieceOffset := binary.BigEndian.Uint32(msg.data[9:13])

				break

			case Piece:
				mngr.PieceManagerSendCh <- piece.Block{
					Index:  binary.BigEndian.Uint32(msg.data[5:9]),
					Offset: binary.BigEndian.Uint32(msg.data[9:13]),
					Data:   msg.data[13:],
				}
			}
		case <-time.After(time.Second * 120):
		case <-ctx.Done():
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
		err = fmt.Errorf("incorrect handshake size: expected 68, got %d", Handshake.Len())
		return err
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
		return fmt.Errorf("failed to write to peer: %w", err)
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
		return fmt.Errorf("couldn't read handshake: %w", err)
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
	p.Conn = conn

	return nil
}

func readLoop(wg *sync.WaitGroup, p *Peer, ctx context.Context, recvCh chan<- PeerMessage) {

	defer wg.Done()

	if ctx == nil {
		ctx = context.Background()
	}

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

func writeLoop(wg *sync.WaitGroup, p *Peer, ctx context.Context, sendCh <-chan []byte) {

	defer wg.Done()

	if ctx == nil {
		ctx = context.Background()
	}

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
