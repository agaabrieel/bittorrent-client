package peer

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	messaging "github.com/agaabrieel/bittorrent-client/pkg/messaging"
	"github.com/bits-and-blooms/bitset"
	"github.com/google/uuid"
)

const HANDSHAKE_SIZE int = 68

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
	PeerBitfield    *bitset.BitSet
	OurBitfield     *bitset.BitSet
	IsInterested    bool
	IsInteresting   bool
	IsChoked        bool
	IsChoking       bool
	PiecesRequested []PeerMessage
	LastActive      time.Time
	LastMessage     PeerMessage
	SentMessages    map[string]bool
	wg              *sync.WaitGroup
	mu              *sync.RWMutex
	RecvCh          <-chan messaging.Message
	BlocksToRequest chan messaging.BlockRequestPayload
}

func NewPeerManager(r *messaging.Router, conn net.Conn, addr net.Addr, wg *sync.WaitGroup) *PeerManager {

	id, ch := uuid.New().String(), make(chan messaging.Message, 1024)

	r.RegisterComponent(id, ch)

	return &PeerManager{
		id:              id,
		PeerConn:        conn,
		PeerAddr:        addr,
		mu:              &sync.RWMutex{},
		wg:              wg,
		RecvCh:          ch,
		BlocksToRequest: make(chan messaging.BlockRequestPayload, 1024),
	}
}

func (p *PeerManager) startPeerHandshake(ctx context.Context, infohash [20]byte, clientId [20]byte) {

	p.wg.Add(1)
	defer p.wg.Done()

	childCtx, ctxCancel := context.WithCancel(ctx)
	defer ctxCancel()

	// Dials connection
	conn, err := net.Dial("tcp", p.PeerAddr.String())
	if err != nil {
		p.Router.Send("peer_orchestrator", messaging.Message{
			SourceId:    p.id,
			PayloadType: messaging.Error,
			Payload: messaging.ErrorPayload{
				Message:     fmt.Sprintf("connection dialing failed: %s", err.Error()),
				Severity:    messaging.Warning,
				Time:        time.Now(),
				ComponentId: p.id,
			},
			CreatedAt: time.Now(),
		})
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

	if Handshake.Len() != HANDSHAKE_SIZE {
		p.Router.Send("peer_orchestrator", messaging.Message{
			SourceId:    p.id,
			PayloadType: messaging.Error,
			Payload: messaging.ErrorPayload{
				Message:     fmt.Sprintf("incorrect handshake size: expected 68, got %d", Handshake.Len()),
				Severity:    messaging.Warning,
				Time:        time.Now(),
				ComponentId: p.id,
			},
			CreatedAt: time.Now(),
		})
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
			p.Router.Send("peer_orchestrator", messaging.Message{
				SourceId:    p.id,
				PayloadType: messaging.Error,
				Payload: messaging.ErrorPayload{
					Message:     fmt.Sprintf("failed to write to peer: %s", err.Error()),
					Severity:    messaging.Warning,
					Time:        time.Now(),
					ComponentId: p.id,
				},
				CreatedAt: time.Now(),
			})
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
	responseBuffer := make([]byte, HANDSHAKE_SIZE)
	readBytes := 0
	for readBytes < len(responseBuffer) {
		n, err := conn.Read(responseBuffer[readBytes:])
		if err != nil {
			p.Router.Send("peer_orchestrator", messaging.Message{
				SourceId:    p.id,
				PayloadType: messaging.Error,
				Payload: messaging.ErrorPayload{
					Message:     fmt.Sprintf("failed to write to peer: %s", err.Error()),
					Severity:    messaging.Warning,
					Time:        time.Now(),
					ComponentId: p.id,
				},
				CreatedAt: time.Now(),
			})
			return
		}
		readBytes += n
	}

	if !bytes.Equal(responseBuffer[0:28], Handshake.Bytes()[:28]) {
		p.Router.Send("peer_orchestrator", messaging.Message{
			SourceId:    p.id,
			PayloadType: messaging.Error,
			Payload: messaging.ErrorPayload{
				Message:     fmt.Sprintf("incorrect handshake, expected %v, got %v", responseBuffer[0:28], Handshake.Bytes()[:28]),
				Severity:    messaging.Warning,
				Time:        time.Now(),
				ComponentId: p.id,
			},
			CreatedAt: time.Now(),
		})
		return
	}

	if !bytes.Equal(responseBuffer[28:48], Handshake.Bytes()[28:48]) {
		p.Router.Send("peer_orchestrator", messaging.Message{
			SourceId:    p.id,
			PayloadType: messaging.Error,
			Payload: messaging.ErrorPayload{
				Message:     fmt.Sprintf("infohash doesn't match, expected %v, got %v", responseBuffer[28:48], Handshake.Bytes()[28:48]),
				Severity:    messaging.Warning,
				Time:        time.Now(),
				ComponentId: p.id,
			},
			CreatedAt: time.Now(),
		})
		return
	}

	peerID := *(*[20]byte)(responseBuffer[48:68])
	if p.PeerId != peerID && p.PeerId != [20]byte{} {
		p.Router.Send("peer_orchestrator", messaging.Message{
			SourceId:    p.id,
			PayloadType: messaging.Error,
			Payload: messaging.ErrorPayload{
				Message:     fmt.Sprintf("peer id changed, expected %v, got %v", p.PeerId, peerID),
				Severity:    messaging.Warning,
				Time:        time.Now(),
				ComponentId: p.id,
			},
			CreatedAt: time.Now(),
		})
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

	p.mainLoop(childCtx)
}

func (p *PeerManager) replyToPeerHandshake(ctx context.Context, infohash [20]byte, clientId [20]byte) {

	p.wg.Add(1)
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
			p.Router.Send("peer_orchestrator", messaging.Message{
				SourceId:    p.id,
				PayloadType: messaging.Error,
				Payload: messaging.ErrorPayload{
					Message:     fmt.Sprintf("failed to read from peer: %s", err.Error()),
					Severity:    messaging.Warning,
					Time:        time.Now(),
					ComponentId: p.id,
				},
				CreatedAt: time.Now(),
			})
			p.PeerConn.Close()
			return
		}
		readBytes += n
	}

	if len(handshakeBuffer) != 68 {
		p.Router.Send("peer_orchestrator", messaging.Message{
			SourceId:    p.id,
			PayloadType: messaging.Error,
			Payload: messaging.ErrorPayload{
				Message:     fmt.Sprintf("incorrect handshake size, got %d", len(handshakeBuffer)),
				Severity:    messaging.Warning,
				Time:        time.Now(),
				ComponentId: p.id,
			},
			CreatedAt: time.Now(),
		})
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
		p.Router.Send("peer_orchestrator", messaging.Message{
			SourceId:    p.id,
			PayloadType: messaging.Error,
			Payload: messaging.ErrorPayload{
				Message:     fmt.Sprintf("incorrect handshake, expected %v, got %v", handshakeBuffer[0:28], handshake.Bytes()[:28]),
				Severity:    messaging.Warning,
				Time:        time.Now(),
				ComponentId: p.id,
			},
			CreatedAt: time.Now(),
		})
		p.PeerConn.Close()
		return
	}

	if !bytes.Equal(handshakeBuffer[28:48], handshake.Bytes()[28:48]) {
		p.Router.Send("peer_orchestrator", messaging.Message{
			SourceId:    p.id,
			PayloadType: messaging.Error,
			Payload: messaging.ErrorPayload{
				Message:     fmt.Sprintf("infohash doesn't match, expected %v, got %v", handshakeBuffer[28:48], handshake.Bytes()[28:48]),
				Severity:    messaging.Warning,
				Time:        time.Now(),
				ComponentId: p.id,
			},
			CreatedAt: time.Now(),
		})
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
			p.Router.Send("peer_orchestrator", messaging.Message{
				SourceId:    p.id,
				PayloadType: messaging.Error,
				Payload: messaging.ErrorPayload{
					Message:     fmt.Sprintf("failed to write to socket: %s", err.Error()),
					Severity:    messaging.Warning,
					Time:        time.Now(),
					ComponentId: p.id,
				},
				CreatedAt: time.Now(),
			})
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

	p.mainLoop(childCtx)
}

func (p *PeerManager) mainLoop(ctx context.Context) {

	p.wg.Add(1)
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
		p.writeLoop(childCtx, peerConnSendCh)
	}()

	// Reader loop
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.readLoop(childCtx, peerConnRecvCh)
	}()

	bitfieldMsg, err := generateBitfieldMsg(*p.OurBitfield)
	if err != nil {
		p.Router.Send("peer_orchestrator", messaging.Message{
			SourceId:    p.id,
			PayloadType: messaging.Error,
			Payload: messaging.ErrorPayload{
				Message:     fmt.Sprintf("failed to generate bitfield message: %s", err.Error()),
				Severity:    messaging.Warning,
				Time:        time.Now(),
				ComponentId: p.id,
			},
			CreatedAt: time.Now(),
		})
		return
	}

	peerConnSendCh <- bitfieldMsg

	// PEER MESSAGE LISTENER
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			select {
			case <-childCtx.Done():
				return

			case msg := <-peerConnRecvCh:
				p.wg.Add(1)
				go func() {

					p.mu.Lock()
					defer p.mu.Unlock()
					defer p.wg.Done()

					p.LastMessage = msg
					p.LastActive = time.Now()

					switch msg.messageType {

					// First 4 cases simply update the internal peer state
					case Choke:
						p.IsChoking = true

					case Unchoke:
						p.IsChoking = false

					case NotInterested:
						p.IsInterested = false

					case Interested:
						p.IsInterested = true

					case Bitfield:

						bs := make([]uint64, len(msg.data)/8)
						for i := range len(bs) {
							bs[i] = binary.BigEndian.Uint64(msg.data[0+i*8 : 8+i*8])
						}

						p.PeerBitfield = bitset.From(bs)
						wantedPieces := p.PeerBitfield.Difference(p.OurBitfield)

						if wantedPieces.Any() {
							if !p.IsInteresting {
								peerConnSendCh <- generateNoPayloadMsg(Interested)
								p.IsInteresting = true
							}
						} else if p.IsInteresting {
							peerConnSendCh <- generateNoPayloadMsg(NotInterested)
							p.IsInteresting = false
						}

						msgId := uuid.NewString()
						p.SentMessages[msgId] = true
						p.Router.Send("peer_orchestrator", messaging.Message{
							Id:          msgId,
							SourceId:    p.id,
							ReplyTo:     p.id,
							PayloadType: messaging.PeerBitfield,
							Payload: messaging.PeerBitfieldPayload{
								Bitfield: p.PeerBitfield,
							},
							CreatedAt: time.Now(),
						})

					case Have:

						pieceIndex := binary.BigEndian.Uint32(msg.data[5:])
						p.PeerBitfield.Set(uint(pieceIndex))

						wantedPieces := p.PeerBitfield.Difference(p.OurBitfield)

						if wantedPieces.Any() {
							if !p.IsInteresting {
								peerConnSendCh <- generateNoPayloadMsg(Interested)
								p.IsInteresting = true
							}
						} else if p.IsInteresting {
							peerConnSendCh <- generateNoPayloadMsg(NotInterested)
							p.IsInteresting = false
						}

						msgId := uuid.NewString()
						p.SentMessages[msgId] = true
						p.Router.Send("peer_orchestrator", messaging.Message{
							Id:          msgId,
							SourceId:    p.id,
							ReplyTo:     p.id,
							PayloadType: messaging.PeerBitfieldUpdate,
							Payload: messaging.PeerBitfieldUpdatePayload{
								Index: int(pieceIndex),
							},
							CreatedAt: time.Now(),
						})

					case Request:

						msgId := uuid.NewString()
						p.SentMessages[msgId] = true

						p.Router.Send("piece_manager", messaging.Message{
							Id:          msgId,
							SourceId:    p.id,
							ReplyTo:     p.id,
							ReplyingTo:  "",
							PayloadType: messaging.BlockRequest,
							Payload: messaging.BlockRequestPayload{
								Index:  binary.BigEndian.Uint32(msg.data[5:9]),
								Offset: binary.BigEndian.Uint32(msg.data[9:13]),
								Size:   binary.BigEndian.Uint32(msg.data[13:17]),
							},
							CreatedAt: time.Now(),
						})

					case Piece:

						msgId := uuid.NewString()
						p.SentMessages[msgId] = true

						p.Router.Send("piece_manager", messaging.Message{
							Id:          msgId,
							SourceId:    p.id,
							ReplyTo:     p.id,
							ReplyingTo:  "",
							PayloadType: messaging.BlockSend,
							Payload: messaging.BlockSendPayload{
								Index:  binary.BigEndian.Uint32(msg.data[5:9]),
								Offset: binary.BigEndian.Uint32(msg.data[9:13]),
								Size:   uint32(len(msg.data[13:])),
								Data:   msg.data[13:],
							},
							CreatedAt: time.Now(),
						})
					}
				}()
			}
		}
	}()

	// ROUTER LISTENING LOOP
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			select {
			case <-childCtx.Done():
				// LOG STUFF
				return

			case msg := <-p.RecvCh:

				if msg.ReplyingTo != "" {
					p.mu.Lock()
					exists := p.SentMessages[msg.ReplyingTo]
					if !exists {
						p.Router.Send("peer_orchestrator", messaging.Message{
							SourceId:    p.id,
							ReplyingTo:  msg.Id,
							PayloadType: messaging.Error,
							Payload: messaging.ErrorPayload{
								Message:     fmt.Sprintf("unexpected message with type %v replying to %s", msg.PayloadType, msg.ReplyingTo),
								Severity:    messaging.Warning,
								ErrorCode:   messaging.ErrCodeUnexpectedMessage,
								Time:        time.Now(),
								ComponentId: p.id,
							},
							CreatedAt: time.Now(),
						})
						continue
					}
					delete(p.SentMessages, msg.ReplyingTo)
					p.mu.Unlock()
				}

				if msg.ReplyTo != "" {
					p.Router.Send(msg.ReplyTo, messaging.Message{
						Id:          uuid.NewString(),
						SourceId:    p.id,
						ReplyingTo:  msg.Id,
						PayloadType: messaging.Acknowledged,
						Payload:     nil,
						CreatedAt:   time.Now(),
					})
				}

				switch msg.PayloadType {
				case messaging.BlockSend:

					payload, ok := msg.Payload.(messaging.BlockSendPayload)
					if !ok {
						p.Router.Send("peer_orchestrator", messaging.Message{
							SourceId:    p.id,
							PayloadType: messaging.Error,
							Payload: messaging.ErrorPayload{
								Message:     fmt.Sprintf("unexpected payload, expected BlockSendPayload, got %v", msg.PayloadType),
								Severity:    messaging.Warning,
								ErrorCode:   messaging.ErrCodeInvalidPayload,
								Time:        time.Now(),
								ComponentId: p.id,
							},
							CreatedAt: time.Now(),
						})
						return
					}

					if p.IsInterested && !p.IsChoking {
						peerConnSendCh <- generatePieceMsg(payload.Index, payload.Offset, payload.Data)
					} else {

						p.Router.Send("peer_orchestrator", messaging.Message{
							SourceId:    p.id,
							PayloadType: messaging.Error,
							Payload: messaging.ErrorPayload{
								Message:     "peer is not interested or choking",
								Severity:    messaging.Warning,
								ErrorCode:   messaging.ErrCodeInvalidPayload,
								Time:        time.Now(),
								ComponentId: p.id,
							},
							CreatedAt: time.Now(),
						})

					}

				case messaging.PieceValidated:

					payload, ok := msg.Payload.(messaging.PieceValidatedPayload)
					if !ok {
						p.Router.Send("peer_orchestrator", messaging.Message{
							SourceId:    p.id,
							PayloadType: messaging.Error,
							Payload: messaging.ErrorPayload{
								Message:     fmt.Sprintf("unexpected payload, expected PieceValidatedPayload, got %v", msg.PayloadType),
								Severity:    messaging.Warning,
								ErrorCode:   messaging.ErrCodeInvalidPayload,
								Time:        time.Now(),
								ComponentId: p.id,
							},
							CreatedAt: time.Now(),
						})
					}

					p.mu.Lock()
					p.OurBitfield.Set(uint(payload.Index))
					peerConnSendCh <- generateHaveMsg(payload.Index)
					p.mu.Unlock()

				case messaging.NextBlockIndexSend:

					payload, ok := msg.Payload.(messaging.NextBlockIndexSendPayload)
					if !ok {
						p.Router.Send("peer_orchestrator", messaging.Message{
							SourceId:    p.id,
							PayloadType: messaging.Error,
							Payload: messaging.ErrorPayload{
								Message:     fmt.Sprintf("unexpected payload, expected NextBlockIndexSendPayload, got %v", msg.PayloadType),
								Severity:    messaging.Warning,
								ErrorCode:   messaging.ErrCodeInvalidPayload,
								Time:        time.Now(),
								ComponentId: p.id,
							},
							CreatedAt: time.Now(),
						})
					}

					p.BlocksToRequest <- messaging.BlockRequestPayload{
						Index:  uint32(payload.PieceIndex),
						Offset: uint32(payload.Offset),
						Size:   uint32(payload.Size),
					}

				}
			}
		}
	}()

	// PIECE REQUEST LOOP
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			select {
			case <-childCtx.Done():
				// stuff
				return
			case block := <-p.BlocksToRequest:
				if p.IsInteresting && !p.IsChoking {
					peerConnSendCh <- generateRequestMsg(block.Index, block.Offset, block.Size)
				}
			}
		}
	}()

	select {
	case <-ctx.Done():
		ctxCancel()
		return
	default:

		msgId := uuid.NewString()
		p.SentMessages[msgId] = true

		p.Router.Send("", messaging.Message{
			Id:          msgId,
			SourceId:    p.id,
			ReplyTo:     p.id,
			ReplyingTo:  "",
			PayloadType: messaging.NextBlockIndexRequest,
			Payload: messaging.NextBlockIndexRequestPayload{
				PieceIndex: 1,
			},
			CreatedAt: time.Now(),
		})
	}
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
					p.Router.Send("peer_orchestrator", messaging.Message{
						SourceId:    p.id,
						PayloadType: messaging.Error,
						Payload: messaging.ErrorPayload{
							Message:     fmt.Sprintf("reading timed-out, closing connection: %s", err.Error()),
							Severity:    messaging.Warning,
							ErrorCode:   messaging.ErrCodeInvalidPayload,
							Time:        time.Now(),
							ComponentId: p.id,
						},
						CreatedAt: time.Now(),
					})
					return
				}

				p.Router.Send("peer_orchestrator", messaging.Message{
					SourceId:    p.id,
					PayloadType: messaging.Error,
					Payload: messaging.ErrorPayload{
						Message:     fmt.Sprintf("read error: %s", err.Error()),
						Severity:    messaging.Warning,
						ErrorCode:   messaging.ErrCodeInvalidPayload,
						Time:        time.Now(),
						ComponentId: p.id,
					},
					CreatedAt: time.Now(),
				})
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
					p.Router.Send("peer_orchestrator", messaging.Message{
						SourceId:    p.id,
						PayloadType: messaging.Error,
						Payload: messaging.ErrorPayload{
							Message:     fmt.Sprintf("buffer read error: %s", err.Error()),
							Severity:    messaging.Warning,
							ErrorCode:   messaging.ErrCodeInvalidPayload,
							Time:        time.Now(),
							ComponentId: p.id,
						},
						CreatedAt: time.Now(),
					})
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
						p.Router.Send("peer_orchestrator", messaging.Message{
							SourceId:    p.id,
							PayloadType: messaging.Error,
							Payload: messaging.ErrorPayload{
								Message:     fmt.Sprintf("writer timed out: %s", err.Error()),
								Severity:    messaging.Warning,
								ErrorCode:   messaging.ErrCodeInvalidPayload,
								Time:        time.Now(),
								ComponentId: p.id,
							},
							CreatedAt: time.Now(),
						})
						return

					} else if err == io.EOF {
						return
					}

					p.Router.Send("peer_orchestrator", messaging.Message{
						SourceId:    p.id,
						PayloadType: messaging.Error,
						Payload: messaging.ErrorPayload{
							Message:     fmt.Sprintf("write error: %s", err.Error()),
							Severity:    messaging.Warning,
							ErrorCode:   messaging.ErrCodeInvalidConnection,
							Time:        time.Now(),
							ComponentId: p.id,
						},
						CreatedAt: time.Now(),
					})
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

func generateBitfieldMsg(bf bitset.BitSet) ([]byte, error) {
	buf := make([]byte, bf.Len()+5)
	binary.BigEndian.PutUint32(buf, uint32(bf.Len()+1))
	buf[4] = byte(Bitfield)

	binarySet, err := bf.MarshalBinary()
	if err != nil {
		return nil, err
	}

	copy(buf[5:], binarySet)
	return buf, nil
}

func generatePieceMsg(idx uint32, offset uint32, data []byte) []byte {
	buf := make([]byte, len(data)+13)
	binary.BigEndian.PutUint32(buf, uint32(len(data)+9))
	buf[4] = byte(Piece)
	binary.BigEndian.PutUint32(buf[5:9], idx)
	binary.BigEndian.PutUint32(buf[9:13], offset)
	copy(buf[13:], data)
	return buf
}
