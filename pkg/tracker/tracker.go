package tracker

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/agaabrieel/bittorrent-client/pkg/messaging"
	"github.com/agaabrieel/bittorrent-client/pkg/metainfo"
	parser "github.com/agaabrieel/bittorrent-client/pkg/parser"
	"github.com/google/uuid"
)

const ProtocolID uint64 = 0x41727101980
const UDPPacketSize int = 98

type Action uint8

const (
	ConnectAction Action = iota
	AnnounceAction
	ScrapeAction
	ErrorAction
)

type client interface {
	Announce(ctx context.Context, trackerUrl *url.URL, req AnnounceRequest) (*AnnounceResponse, error)
}

type HTTPClient struct {
	client *http.Client
}

type UDPClient struct {
	dialer  *net.Dialer
	connIds map[string]uint64
	mutex   sync.Mutex
}

type AnnounceRequest struct {
	infoHash   [20]byte
	peerID     [20]byte
	port       uint16
	uploaded   uint64
	downloaded uint64
	left       uint64
	event      string // "started", "stopped", "completed"
}

type AnnounceResponse struct {
	tracker  *Tracker
	interval time.Duration // Seconds to wait between announces
	peers    []PeerAddr    // List of peers
}

type Tracker struct {
	client
	url      *url.URL
	interval time.Duration
	peers    []PeerAddr
	peerMap  map[[20]byte]bool
}

type PeerAddr struct {
	id       [20]byte
	protocol string
	ip       net.IP
	port     uint16
}

func (addr PeerAddr) Network() string {
	return addr.protocol
}

func (addr PeerAddr) String() string {
	strPort := strconv.Itoa(int(addr.port))
	return addr.ip.String() + ":" + strPort
}

type TrackerManager struct {
	id                       string
	Tracker                  *Tracker
	Router                   *messaging.Router
	ClientId                 [20]byte
	Metainfo                 *metainfo.TorrentMetainfo
	RecvCh                   <-chan messaging.Message
	TrackerErrCh             chan error
	AnnounceRespCh           chan AnnounceResponse
	IsWaitingForAnnounceData bool
	wg                       *sync.WaitGroup
	mu                       *sync.Mutex
}

func NewTrackerManager(meta *metainfo.TorrentMetainfo, r *messaging.Router, clientId [20]byte) (*TrackerManager, error) {

	id, ch := "tracker_manager", make(chan messaging.Message, 1024)
	err := r.RegisterComponent(id, ch)
	if err != nil {
		return nil, fmt.Errorf("failed to register component with id %v: %v", id, err)
	}

	trackerErrCh := make(chan error, 1024)

	return &TrackerManager{
		id:             id,
		Tracker:        nil,
		Router:         r,
		Metainfo:       meta,
		ClientId:       clientId,
		RecvCh:         ch,
		TrackerErrCh:   trackerErrCh,
		AnnounceRespCh: make(chan AnnounceResponse, 1),
		wg:             &sync.WaitGroup{},
		mu:             &sync.Mutex{},
	}, nil
}

func (mngr *TrackerManager) Run(ctx context.Context, wg *sync.WaitGroup) {

	childCtx, ctxCancel := context.WithCancel(ctx)
	defer ctxCancel()
	defer wg.Done()

	err := mngr.setupTracker(childCtx)
	if err != nil {
		mngr.Router.Send("error_handler", messaging.Message{
			Id:          uuid.NewString(),
			SourceId:    mngr.id,
			PayloadType: messaging.Error,
			Payload: messaging.ErrorPayload{
				Message:     fmt.Sprintf("failed to setup tracker: %s", err.Error()),
				Severity:    messaging.Critical,
				ErrorCode:   messaging.ErrCodeInvalidTracker,
				Time:        time.Now(),
				ComponentId: "tracker_manager",
			},
			CreatedAt: time.Now(),
		})
		return
	}

	if mngr.Tracker == nil {
		mngr.Router.Send("error_handler", messaging.Message{
			Id:          uuid.NewString(),
			SourceId:    mngr.id,
			PayloadType: messaging.Error,
			Payload: messaging.ErrorPayload{
				Message:     "tracker is nil",
				Severity:    messaging.Critical,
				ErrorCode:   messaging.ErrCodeInvalidTracker,
				Time:        time.Now(),
				ComponentId: "tracker_manager",
			},
			CreatedAt: time.Now(),
		})
		return
	}

	for _, peerAddr := range mngr.Tracker.peers {
		mngr.Router.Send("peer_orchestrator", messaging.Message{
			SourceId:    mngr.id,
			ReplyTo:     mngr.id,
			PayloadType: messaging.PeersDiscovered,
			Payload:     peerAddr,
			CreatedAt:   time.Now(),
		})
	}

	trackerInterval := time.NewTimer(mngr.Tracker.interval)
	for {

		select {
		case msg := <-mngr.RecvCh:

			switch msg.PayloadType {

			case messaging.AnnounceDataSend:

				payload, ok := msg.Payload.(messaging.AnnounceDataSendPayload)
				if !ok {
					mngr.Router.Send("error_handler", messaging.Message{
						Id:          uuid.NewString(),
						SourceId:    mngr.id,
						PayloadType: messaging.Error,
						Payload: messaging.ErrorPayload{
							Message:     "incorrect payload",
							Severity:    messaging.Critical,
							Time:        time.Now(),
							ComponentId: "tracker_manager",
						},
						CreatedAt: time.Now(),
					})
					return
				}

				port, err := strconv.Atoi(mngr.Tracker.url.Port())
				if err != nil {
					mngr.Router.Send("error_handler", messaging.Message{
						Id:          uuid.NewString(),
						SourceId:    mngr.id,
						PayloadType: messaging.Error,
						Payload: messaging.ErrorPayload{
							Message:     fmt.Sprintf("failed to convert port to int: %s", err.Error()),
							Severity:    messaging.Critical,
							Time:        time.Now(),
							ComponentId: "tracker_manager",
						},
						CreatedAt: time.Now(),
					})
					return
				}

				req := AnnounceRequest{
					infoHash:   mngr.Metainfo.Infohash,
					peerID:     mngr.ClientId,
					port:       uint16(port),
					uploaded:   payload.Uploaded,
					downloaded: payload.Downloaded,
					left:       payload.Left,
					event:      payload.Event,
				}

				mngr.wg.Add(1)
				go mngr.Tracker.Announce(childCtx, mngr.wg, req, mngr.AnnounceRespCh, mngr.TrackerErrCh)
			}

		case msg := <-mngr.AnnounceRespCh:

			mngr.IsWaitingForAnnounceData = false
			trackerInterval.Reset(msg.interval)

			newPeers := make([]net.Addr, 256)
			for _, peer := range msg.peers {
				if !mngr.Tracker.peerMap[peer.id] {
					mngr.Tracker.peers = append(mngr.Tracker.peers, peer)
					mngr.Tracker.peerMap[peer.id] = true
					newPeers = append(newPeers, peer)
				}
			}

			mngr.Router.Send("peer_orchestrator", messaging.Message{
				SourceId:    mngr.id,
				ReplyTo:     mngr.id,
				PayloadType: messaging.PeersDiscovered,
				Payload: messaging.PeersDiscoveredPayload{
					Addrs: newPeers,
				},
				CreatedAt: time.Now(),
			})

		case <-trackerInterval.C:

			if !mngr.IsWaitingForAnnounceData {

				mngr.Router.Send("", messaging.Message{
					SourceId:    mngr.id,
					ReplyTo:     mngr.id,
					PayloadType: messaging.AnnounceDataRequest,
					Payload:     nil,
					CreatedAt:   time.Now(),
				})

				mngr.IsWaitingForAnnounceData = true

			}

		case <-ctx.Done():
			return
		}
	}
}

func (mngr *TrackerManager) setupTracker(ctx context.Context) error {

	mngr.mu.Lock()
	defer mngr.mu.Unlock()

	req := AnnounceRequest{
		infoHash:   mngr.Metainfo.Infohash,
		peerID:     mngr.ClientId,
		port:       6881, // READ FROM MSG
		uploaded:   0,    // READ FROM MSG
		downloaded: 0,    // READ FROM MSG
		left:       mngr.Metainfo.InfoDict.Length,
		event:      "started",
	}

	trackerResponseCh := make(chan AnnounceResponse, 1)

	var trackerURLs []string
	if mngr.Metainfo.AnnounceList != nil {
		for _, trackers := range mngr.Metainfo.AnnounceList {
			trackerURLs = append(trackerURLs, trackers...)
		}
	} else if mngr.Metainfo.Announce != "" {
		trackerURLs = append(trackerURLs, mngr.Metainfo.Announce)
	}

	for _, tracker := range trackerURLs {
		trackerUrl, err := url.Parse(tracker)

		if err != nil {
			mngr.Router.Send("error_handler", messaging.Message{
				Id:          uuid.NewString(),
				SourceId:    mngr.id,
				PayloadType: messaging.Error,
				Payload: messaging.ErrorPayload{
					Message:     fmt.Sprintf("failed to parse tracker url: %s", err.Error()),
					Severity:    messaging.Critical,
					Time:        time.Now(),
					ComponentId: mngr.id,
				},
				CreatedAt: time.Now(),
			})
			continue
		}

		tracker := NewTracker(trackerUrl)
		childCtx, ctxCancel := context.WithTimeout(ctx, 30*time.Second)
		defer ctxCancel()

		var port int
		switch trackerUrl.Scheme {
		case "udp":
			port, err = strconv.Atoi(trackerUrl.Port())
			if err != nil {
				mngr.Router.Send("error_handler", messaging.Message{
					Id:          uuid.NewString(),
					SourceId:    mngr.id,
					PayloadType: messaging.Error,
					Payload: messaging.ErrorPayload{
						Message:     fmt.Sprintf("failed to parse tracker url: %s", err.Error()),
						Severity:    messaging.Critical,
						Time:        time.Now(),
						ComponentId: mngr.id,
					},
					CreatedAt: time.Now(),
				})

				continue
			}
		case "http":
			port = 80
		case "https":
			port = 443
		}

		req.port = uint16(port)

		mngr.wg.Add(1)
		go tracker.Announce(childCtx, mngr.wg, req, trackerResponseCh, mngr.TrackerErrCh)

		select {
		case trackerResponse := <-trackerResponseCh:

			mngr.wg.Wait()
			close(trackerResponseCh)

			if trackerResponse.tracker == nil || len(trackerResponse.peers) == 0 {
				mngr.Router.Send("error_handler", messaging.Message{
					Id:          uuid.NewString(),
					SourceId:    mngr.id,
					PayloadType: messaging.Error,
					Payload: messaging.ErrorPayload{
						Message:     "tracker response is nil",
						Severity:    messaging.Critical,
						Time:        time.Now(),
						ComponentId: mngr.id,
					},
					CreatedAt: time.Now(),
				})
				continue
			}

			mngr.Tracker = trackerResponse.tracker
			mngr.Tracker.interval = trackerResponse.interval
			mngr.Tracker.peers = trackerResponse.peers
			for _, p := range mngr.Tracker.peers {
				mngr.Tracker.peerMap[p.id] = true
			}

			return nil

		case <-time.After(30 * time.Second):
			mngr.Router.Send("error_handler", messaging.Message{
				Id:          uuid.NewString(),
				SourceId:    mngr.id,
				PayloadType: messaging.Error,
				Payload: messaging.ErrorPayload{
					Message:     "tracker response timed out",
					Severity:    messaging.Critical,
					Time:        time.Now(),
					ComponentId: mngr.id,
				},
				CreatedAt: time.Now(),
			})
			continue
		case <-ctx.Done():

			mngr.Router.Send("error_handler", messaging.Message{
				Id:          uuid.NewString(),
				SourceId:    mngr.id,
				PayloadType: messaging.Error,
				Payload: messaging.ErrorPayload{
					Message:     "tracker response timed out",
					Severity:    messaging.Critical,
					Time:        time.Now(),
					ComponentId: mngr.id,
				},
				CreatedAt: time.Now(),
			})

			mngr.Tracker.interval = 0
			mngr.Tracker.peers = nil
			mngr.Tracker.peerMap = nil
			mngr.Tracker = nil
			mngr.wg.Wait()
			close(trackerResponseCh)
			mngr.AnnounceRespCh = nil
			mngr.RecvCh = nil
			mngr.ClientId = [20]byte{}
			mngr.Metainfo = nil
			mngr.IsWaitingForAnnounceData = false
			mngr.wg = nil
			mngr.mu = nil
			mngr.Router = nil
			mngr.id = ""

			return fmt.Errorf("tracker response timed out")
		}
	}
	return nil
}

func NewTracker(trackerUrl *url.URL) *Tracker {

	var tc client
	if trackerUrl.Scheme == "udp" {
		tc = &UDPClient{
			dialer: &net.Dialer{
				Timeout: time.Duration(time.Second * 45),
			},
			connIds: make(map[string]uint64),
		}
	} else if trackerUrl.Scheme == "http" || trackerUrl.Scheme == "https" {
		tc = &HTTPClient{
			client: &http.Client{
				Timeout: time.Duration(time.Second * 45),
			},
		}
	} else {
		return &Tracker{}
	}

	return &Tracker{
		client:   tc,
		url:      trackerUrl,
		interval: time.Duration(0),
		peerMap:  make(map[[20]byte]bool),
	}
}

func (t *Tracker) Announce(ctx context.Context, wg *sync.WaitGroup, req AnnounceRequest, respCh chan<- AnnounceResponse, errCh chan error) {

	childCtx, ctxCancel := context.WithCancel(ctx)
	defer ctxCancel()

	defer wg.Done()

	if t.client == nil {
		errCh <- errors.New("tracker does not implement client")
		return
	}

	resp, err := t.client.Announce(childCtx, t.url, req)
	if err != nil {
		errCh <- fmt.Errorf("failed tracker announce: %v", err)
		return
	}

	if resp == nil {
		errCh <- errors.New("tracker response is nil")
		return
	}

	resp.tracker = t

	select {
	case respCh <- *resp:
	case <-ctx.Done():
	default:
		// If the channel is full, we don't care about the response anymore
	}
}

func (c *UDPClient) Announce(ctx context.Context, trackerUrl *url.URL, req AnnounceRequest) (*AnnounceResponse, error) {

	childCtx, ctxCancel := context.WithCancel(ctx)
	defer ctxCancel()

	var announceResp *AnnounceResponse

	if c.dialer == nil {
		return nil, fmt.Errorf("client has no valid dialer")
	}

	serverAddr, err := net.ResolveUDPAddr("udp", trackerUrl.Host)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve UDP address: %w", err)
	}

	conn, err := c.dialer.DialContext(childCtx, "udp", serverAddr.String())
	if err != nil {
		return nil, fmt.Errorf("failed to connect to tracker: %w", err)
	}
	defer conn.Close()

	c.mutex.Lock()
	cachedConnId, found := c.connIds[trackerUrl.Host]
	c.mutex.Unlock()

	if found { // IF CONNECTION ID ALREADY EXISTS, SKIP CONNECTION REQUEST
		connId := cachedConnId
		announceResp, err = c.makeAnnounceRequest(childCtx, connId, req, conn)
		if err != nil {
			err = fmt.Errorf("failed to make announce request: %w", err)
			errors.Join(err, errors.New("connection id may have expired, retrying"))

			c.mutex.Lock()
			if currentId, stillExists := c.connIds[trackerUrl.Host]; stillExists && currentId == cachedConnId {
				delete(c.connIds, trackerUrl.Host)
			}
			c.mutex.Unlock()

		} else {
			return announceResp, nil
		}
	}

	connId, err := c.makeConnectionRequest(childCtx, conn)
	if err != nil {
		return nil, fmt.Errorf("failed to make connection request: %w", err)
	}

	c.mutex.Lock()
	c.connIds[trackerUrl.Host] = connId
	c.mutex.Unlock()

	announceResp, err = c.makeAnnounceRequest(childCtx, connId, req, conn)
	if err != nil {
		return nil, fmt.Errorf("failed to make announce request: %w", err)
	}

	return announceResp, nil
}

func (c *HTTPClient) Announce(ctx context.Context, trackerUrl *url.URL, req AnnounceRequest) (*AnnounceResponse, error) {

	childCtx, ctxCancel := context.WithCancel(ctx)
	defer ctxCancel()

	if c.client == nil {
		return nil, errors.New("tracker client is nil")
	}

	params := url.Values{}

	params.Set("info_hash", url.QueryEscape(string(req.infoHash[:])))
	params.Set("peer_id", url.QueryEscape(string(req.peerID[:])))
	params.Set("port", strconv.Itoa(int(req.port)))

	uploaded := strconv.Itoa(int(req.uploaded))
	params.Set("uploaded", uploaded)

	dowloaded := strconv.Itoa(int(req.downloaded))
	params.Set("downloaded", dowloaded)

	left := strconv.Itoa(int(req.left))
	params.Set("left", left)

	params.Set("compact", string("1"))
	if req.event != "" {
		params.Set("event", req.event)
	}

	finalUrl := trackerUrl
	finalUrl.RawQuery = params.Encode()

	httpReq, err := http.NewRequestWithContext(childCtx, http.MethodGet, finalUrl.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request context: %w", err)
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to make tracker requerst: %w", err)
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read tracker response: %w", err)
	}

	parserCtx, err := parser.NewParserContext(b)
	if err != nil {
		return nil, fmt.Errorf("parser context creation failed: %w", err)
	}

	rootDict, err := parserCtx.Parse()
	if err != nil {
		return nil, fmt.Errorf("response parsing failed: %w", err)
	}

	if rootDict.ValueType != parser.BencodeDict {
		return nil, fmt.Errorf("invalid response format: %w", err)
	}

	var warning string
	for _, entry := range rootDict.DictValue {
		key, err := entry.Key.GetStringValue()
		if err != nil {
			return nil, fmt.Errorf("error parsing response key: %w", err)
		}

		switch key {
		case "failure reason":
			val, err := entry.Value.GetStringValue()
			if err != nil {
				return nil, fmt.Errorf("error parsing failure reason: %w", err)
			}
			return nil, fmt.Errorf("tracker returned failure: %v", val)
		case "warning message":
			warning, err = entry.Value.GetStringValue()
			if err != nil {
				return nil, fmt.Errorf("error parsing warning message: %w", err)
			}
		default:
			// Ignore other keys
		}
	}

	announceResp, err := parseAnnounceResponse(rootDict)
	if err != nil {
		return nil, fmt.Errorf("response parsing failed: %w", err)
	}

	return announceResp, fmt.Errorf("warning from tracker: %s", warning)
}

func (c *UDPClient) makeAnnounceRequest(ctx context.Context, connId uint64, req AnnounceRequest, conn net.Conn) (*AnnounceResponse, error) {

	childCtx, ctxCancel := context.WithCancel(ctx)
	defer ctxCancel()

	var announceTransactionIDBytes [4]byte
	if _, err := io.ReadFull(rand.Reader, announceTransactionIDBytes[:]); err != nil {
		return nil, fmt.Errorf("failed to generate announce transaction ID: %w", err)
	}
	announceTransactionId := binary.BigEndian.Uint32(announceTransactionIDBytes[:])

	announceMsg := c.generateAnnounceMsg(announceTransactionId, req, connId)

	if deadline, ok := childCtx.Deadline(); ok {
		conn.SetWriteDeadline(deadline)
	} else {
		conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
	}

	n, err := conn.Write(announceMsg)
	if err != nil {
		return nil, fmt.Errorf("invalid announce response: %w", err)
	}

	if n != UDPPacketSize {
		return nil, fmt.Errorf("wrote %d bytes, expected 98 bytes", n)
	}

	if deadline, ok := childCtx.Deadline(); ok {
		conn.SetReadDeadline(deadline)
	} else {
		conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	}

	announceBuffer := make([]byte, 4096)

	// ANNOUNCE READ //
	n, err = conn.Read(announceBuffer)
	if err != nil {
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			return nil, fmt.Errorf("UDP connect timed out waiting for response from %s: %w", conn.RemoteAddr().String(), err)
		}
		return nil, fmt.Errorf("failed to read UDP connect response: %w", err)
	}

	announceResponseBytes := announceBuffer[:n] // Use actual bytes read

	if len(announceResponseBytes) < 8 { // Need at least action + transactionId for error check
		return nil, fmt.Errorf("announce response too short: got %d bytes, expected at least 8", len(announceResponseBytes))
	}

	action := binary.BigEndian.Uint32(announceBuffer[0:4])
	responseTransactionID := binary.BigEndian.Uint32(announceBuffer[4:8])

	if action == uint32(ErrorAction) {
		errorMsg := "tracker returned error"
		if len(announceResponseBytes) > 8 { // Error message is optional
			errorMsg = fmt.Sprintf("tracker returned error: %s", string(announceResponseBytes[8:]))
		}
		return nil, errors.New(errorMsg)
	} else if action != uint32(AnnounceAction) {
		return nil, fmt.Errorf("expected action announce, got %s", string(announceBuffer[0:4]))
	}

	if n < 20 {
		return nil, fmt.Errorf("tracker responded with %d bytes, expected at least 20 bytes: %w", n, err)
	}

	if responseTransactionID != announceTransactionId {
		return nil, fmt.Errorf("transaction id from response differs from sent id: expected %d, got %d", responseTransactionID, announceTransactionId)
	}

	peerData := announceBuffer[20:n]
	numPeers := len(peerData) / 6
	if len(peerData)%6 != 0 {
		return nil, fmt.Errorf("incomplete peer data received (%d bytes)", len(peerData))
	}

	var announceResp AnnounceResponse
	announceResp.peers = make([]PeerAddr, 0, numPeers)
	announceResp.interval = time.Duration(binary.BigEndian.Uint32(announceBuffer[8:12])) * time.Second

	for i := range numPeers {
		offset := i * 6
		ip := net.IP(peerData[offset : offset+4])
		port := binary.BigEndian.Uint16(peerData[offset+4 : offset+6])
		announceResp.peers = append(announceResp.peers, PeerAddr{
			ip:   ip,
			port: port,
		})
	}

	return &announceResp, nil
}

func (c *UDPClient) makeConnectionRequest(ctx context.Context, conn net.Conn) (uint64, error) {

	childCtx, ctxCancel := context.WithCancel(ctx)
	defer ctxCancel()

	msg := make([]byte, 16) // creates buffer for connect  message

	var connectTransactionId uint32
	binary.BigEndian.PutUint64(msg[0:8], ProtocolID)
	binary.BigEndian.PutUint32(msg[8:12], uint32(ConnectAction))
	binary.BigEndian.PutUint32(msg[12:16], connectTransactionId)

	if deadline, ok := childCtx.Deadline(); ok {
		conn.SetWriteDeadline(deadline)
	} else {
		conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	}

	n, err := conn.Write(msg)
	if err != nil {
		return 0, fmt.Errorf("failed to connect to udp tracker: %w", err)
	}

	if n != 16 {
		return 0, fmt.Errorf("udp tracker wrote %d bytes, expected 16", n)
	}

	if deadline, ok := childCtx.Deadline(); ok {
		conn.SetReadDeadline(deadline)
	} else {
		conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	}

	// CONNECT READ //
	connectBuffer := make([]byte, 16)
	n, err = conn.Read(connectBuffer)
	// --------------------------- //

	if err != nil {
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			return 0, fmt.Errorf("UDP connect timed out waiting for response from %s: %w", conn.RemoteAddr().String(), err)
		}
		return 0, fmt.Errorf("failed to read UDP connect response: %w", err)
	}

	if n < 16 {
		return 0, fmt.Errorf("UDP connect response too short: received %d bytes, expected at least 16", n)
	}

	responseAction := binary.BigEndian.Uint32(connectBuffer[0:4])
	responseTransactionID := binary.BigEndian.Uint32(connectBuffer[4:8])
	connectionID := binary.BigEndian.Uint64(connectBuffer[8:16])

	if responseTransactionID != connectTransactionId {
		return 0, fmt.Errorf("transaction id from response differs from sent id: expected %d, got %d", connectTransactionId, responseTransactionID)
	}

	if responseAction != uint32(ConnectAction) {
		if responseAction == uint32(ErrorAction) && len(connectBuffer) >= 8 {
			return 0, fmt.Errorf("tracker returned err message: %s", string(connectBuffer[8:]))
		}
		return 0, fmt.Errorf("tracker responded with action %d, expected %d", responseAction, ConnectAction)
	}

	return connectionID, nil

}

func (c *UDPClient) generateAnnounceMsg(transactionId uint32, req AnnounceRequest, connId uint64) []byte {

	var eventCode uint32 = 0 // Default: none
	switch req.event {
	case "completed":
		eventCode = 1
	case "started":
		eventCode = 2
	case "stopped":
		eventCode = 3
	}

	announceMsg := make([]byte, 98)

	binary.BigEndian.PutUint64(announceMsg[0:8], connId)
	binary.BigEndian.PutUint32(announceMsg[8:12], uint32(AnnounceAction))
	binary.BigEndian.PutUint32(announceMsg[12:16], transactionId)
	copy(announceMsg[16:36], req.infoHash[:])
	copy(announceMsg[36:56], req.peerID[:])
	binary.BigEndian.PutUint64(announceMsg[56:64], req.downloaded)
	binary.BigEndian.PutUint64(announceMsg[64:72], req.left)
	binary.BigEndian.PutUint64(announceMsg[72:80], req.uploaded)
	binary.BigEndian.PutUint32(announceMsg[80:84], eventCode)
	binary.BigEndian.PutUint32(announceMsg[84:88], 0)
	binary.BigEndian.PutUint32(announceMsg[88:92], 0)
	binary.BigEndian.PutUint32(announceMsg[92:96], 0xFFFFFFFF) // -1 as an uint32
	binary.BigEndian.PutUint16(announceMsg[96:98], req.port)

	return announceMsg
}

func parseAnnounceResponse(root *parser.BencodeValue) (*AnnounceResponse, error) {

	var resp AnnounceResponse

	for _, entry := range root.DictValue {
		key, err := entry.Key.GetStringValue()
		if err != nil {
			return nil, err
		}

		switch key {

		case "failure":

			val, err := entry.Value.GetStringValue()
			if err != nil {
				return nil, fmt.Errorf("error parsing failure reason: %w", err)
			}

			return nil, fmt.Errorf("tracker returned failure: %v", val)

		case "interval":

			resp.interval = time.Duration(entry.Value.IntegerValue)
			continue

		case "peers":

			if entry.Value.ValueType == parser.BencodeString {
				const peerSize = 6
				peersListSize := len(root.StringValue)

				if peersListSize%peerSize != 0 {
					return nil, fmt.Errorf("invalid compact response format: %w", err)
				}

				for i := 0; i < len(entry.Value.StringValue); i += peerSize {

					ip := net.ParseIP(string(entry.Value.StringValue[i : i+4]))
					port := binary.BigEndian.Uint16(entry.Value.StringValue[i+4 : i+6])

					resp.peers = append(resp.peers, PeerAddr{
						ip:   ip,
						port: port,
					})
				}

			} else {
				for _, peerDict := range entry.Value.ListValue {

					var peerAddr PeerAddr
					for _, entry := range peerDict.DictValue {

						k, err := entry.Key.GetStringValue()
						if err != nil {
							return nil, err
						}

						switch k {
						case "peer id":
							peerAddr.id = [20]byte(entry.Key.StringValue)
							continue
						case "ip":
							peerAddr.ip = net.ParseIP(string(entry.Value.StringValue))
							continue
						case "port":
							peerAddr.port = uint16(entry.Value.IntegerValue)
							continue
						}
					}
					resp.peers = append(resp.peers, peerAddr)

				}
			}
		}
	}
	return &resp, nil
}
