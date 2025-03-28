package peer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	metainfo "github.com/agaabrieel/bittorrent-client/pkg/metainfo"
	piece "github.com/agaabrieel/bittorrent-client/pkg/piece"
)

type PeerMessage uint8

const (
	Choked PeerMessage = iota
	Unchoked
	Interested
	NotInterested
	Have
	Bitfield
	Request
	Piece
	Cancel
)

type PeerManager struct {
	Peers        []Peer
	PieceManager *piece.PieceManager
	Metainfo     *metainfo.TorrentMetainfo
}

type PeerConn struct {
	Conn       net.Conn
	LastActive time.Time
	Status     PeerMessage
}

type Peer struct {
	Id   [20]byte
	Ip   net.IP
	Port uint16
	Conn PeerConn
}

func NewPeerManager(peers []Peer, pieceManager *piece.PieceManager, metainfo *metainfo.TorrentMetainfo) *PeerManager {
	mngr := PeerManager{
		Peers:        peers,
		PieceManager: pieceManager,
		Metainfo:     metainfo,
	}
	return &mngr
}

func (mngr *PeerManager) Init() {

}

func (p *Peer) StartConnection(ctx context.Context, hash [20]byte, id [20]byte) error {

	if ctx == nil {
		ctx = context.Background()
	}

	// Resolves address
	addr, err := net.ResolveTCPAddr("tcp", net.JoinHostPort(p.Ip.String(), strconv.Itoa(int(p.Port))))
	if err != nil {
		return fmt.Errorf("failed to resolve tcp address from peer: %w", err)
	}

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
	p.Conn = PeerConn{
		Conn:       conn,
		LastActive: time.Now(),
		Status:     Choked,
	}

	return nil
}
