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
	peer "github.com/agaabrieel/bittorrent-client/pkg/peers"
)

const ProtocolID uint64 = 0x41727101980

type Action uint8

const (
	ConnectAction Action = iota
	AnnounceAction
	ScrapeAction
	ErrorAction
)

type client interface {
	Announce(ctx context.Context, trackerUrl url.URL, req AnnounceRequest) (AnnounceResponse, error)
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
	interval time.Duration      // Seconds to wait between announces
	peers    []peer.PeerManager // List of peers
	// ... other fields
}

type Tracker struct {
	client
	url      url.URL
	interval time.Duration
	peers    []peer.PeerManager
	peerMap  map[[20]byte]bool
}

type TrackerManager struct {
	Tracker                  *Tracker
	ClientId                 [20]byte
	Metainfo                 metainfo.TorrentMetainfo
	SendCh                   chan<- messaging.Message
	RecvCh                   <-chan messaging.Message
	AnnounceResponseCh       chan AnnounceResponse
	ErrCh                    chan<- error
	IsWaitingForAnnounceData bool
	WaitGroup                sync.WaitGroup
	Mu                       sync.Mutex
	SubcribedMessageTypes    []messaging.MessageType
}

func NewTrackerManager(meta metainfo.TorrentMetainfo, r *messaging.Router, globalCh chan messaging.Message, ourId [20]byte) *TrackerManager {

	recvCh := make(chan messaging.Message, 256)

	r.Subscribe(messaging.AnnounceDataResponse, recvCh)
	r.Subscribe(messaging.PieceInvalidated, recvCh)
	r.Subscribe(messaging.PieceValidated, recvCh)
	r.Subscribe(messaging.FileFinished, recvCh)

	return &TrackerManager{
		Tracker:            nil,
		Metainfo:           meta,
		ClientId:           ourId,
		SendCh:             globalCh,
		RecvCh:             recvCh,
		AnnounceResponseCh: make(chan AnnounceResponse),
		WaitGroup:          sync.WaitGroup{},
		Mu:                 sync.Mutex{},
		ErrCh:              make(chan error),
		SubcribedMessageTypes: []messaging.MessageType{
			messaging.AnnounceDataResponse,
			messaging.FileFinished,
		},
	}
}

func (mngr *TrackerManager) setupTracker() {

	port, err := strconv.Atoi(mngr.Tracker.url.Port())
	if err != nil {
		mngr.ErrCh <- errors.New("failed to parse tracker port, aborting")
		return
	}

	req := AnnounceRequest{
		infoHash:   mngr.Metainfo.Infohash,
		peerID:     mngr.ClientId,
		port:       uint16(port),
		uploaded:   0, // READ FROM MSG
		downloaded: 0, // READ FROM MSG
		left:       mngr.Metainfo.InfoDict.Length,
		event:      "started",
	}

	trackerResponseCh := make(chan AnnounceResponse)
	if len(mngr.Metainfo.AnnounceList) == 0 {

		trackerUrl, err := url.Parse(mngr.Metainfo.Announce)
		if err != nil {
			mngr.ErrCh <- errors.New("failed to parse announce URL")
		}

		tracker := NewTracker(*trackerUrl)

		mngr.WaitGroup.Add(1)
		go tracker.Announce(&mngr.WaitGroup, context.Background(), req, trackerResponseCh, mngr.ErrCh)

		timer := time.NewTimer(15 * time.Second)
		select {
		case trackerResponse := <-trackerResponseCh:
			mngr.WaitGroup.Wait()
			close(trackerResponseCh)
			mngr.Tracker = trackerResponse.tracker
			mngr.Tracker.interval = trackerResponse.interval
			mngr.Tracker.peers = trackerResponse.peers

			for _, p := range mngr.Tracker.peers {
				mngr.Tracker.peerMap[p.PeerId] = true
			}

			timer.Stop()
			break

		case <-timer.C:
			mngr.ErrCh <- errors.New("tracker response timed out")
			timer.Stop()
			return
		}

	} else {
		for _, trackers := range mngr.Metainfo.AnnounceList {
			for _, tracker := range trackers {

				trackerUrl, err := url.Parse(tracker)
				if err != nil {
					mngr.ErrCh <- errors.New("failed to parse announce URL")
				}

				tracker := NewTracker(*trackerUrl)
				mngr.WaitGroup.Add(1)
				go tracker.Announce(&mngr.WaitGroup, context.Background(), req, trackerResponseCh, mngr.ErrCh)
			}

			timer := time.NewTimer(15 * time.Second)
			select {
			case trackerResponse := <-trackerResponseCh:
				mngr.WaitGroup.Wait()
				timer.Stop()
				close(trackerResponseCh)

				mngr.Tracker = trackerResponse.tracker
				mngr.Tracker.interval = trackerResponse.interval
				mngr.Tracker.peers = trackerResponse.peers

				for _, p := range mngr.Tracker.peers {
					mngr.Tracker.peerMap[p.PeerId] = true
				}

			case <-timer.C:
				timer.Stop()
				continue
			}
		}
	}
}

func (mngr *TrackerManager) Run(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	mngr.setupTracker()
	for _, peer := range mngr.Tracker.peers {
		mngr.SendCh <- messaging.Message{
			MessageType: messaging.NewPeerFromTracker,
			Data:        peer,
		}
	}

	lastIterTime := time.Now()
	for {

		var timer time.Timer
		if mngr.Tracker.interval != 0 {
			mngr.Tracker.interval = mngr.Tracker.interval - time.Since(lastIterTime)
			timer = *time.NewTimer(mngr.Tracker.interval)
		} else {
			timer = *time.NewTimer(0)
		}

		select {
		case msg := <-mngr.RecvCh:
			switch msg.MessageType {
			case messaging.AnnounceDataResponse:

				payload, ok := msg.Data.(messaging.AnnounceDataResponseData)
				if !ok {
					mngr.ErrCh <- errors.New("router sent wrong datatype, aborting")
					return
				}

				port, err := strconv.Atoi(mngr.Tracker.url.Port())
				if err != nil {
					mngr.ErrCh <- errors.New("failed to parse tracker port, aborting")
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
				go mngr.Tracker.Announce(&mngr.WaitGroup, ctx, req, mngr.AnnounceResponseCh, mngr.ErrCh)
			}

		case msg := <-mngr.AnnounceResponseCh:

			mngr.IsWaitingForAnnounceData = false
			mngr.Tracker.interval = msg.interval

			for _, peer := range msg.peers {
				if !mngr.Tracker.peerMap[peer.PeerId] {
					mngr.SendCh <- messaging.Message{
						MessageType: messaging.NewPeerFromTracker,
						Data:        peer,
					}
					mngr.Tracker.peers = append(mngr.Tracker.peers, peer)
					mngr.Tracker.peerMap[peer.PeerId] = true
				}
			}

		case <-timer.C:
			if !mngr.IsWaitingForAnnounceData {
				mngr.SendCh <- messaging.Message{
					MessageType: messaging.AnnounceDataRequest,
					Data:        nil,
				}
			}
		}

		lastIterTime = time.Now()
		timer.Stop()

	}

}

func NewTracker(trackerUrl url.URL) Tracker {

	var tc client
	if trackerUrl.Scheme == "udp" {
		tc = &UDPClient{
			dialer: &net.Dialer{
				Timeout: time.Duration(time.Second * 45),
			},
		}
	} else if trackerUrl.Scheme == "http" || trackerUrl.Scheme == "https" {
		tc = &HTTPClient{
			client: &http.Client{
				Timeout: time.Duration(time.Second * 45),
			},
		}
	} else {
		return Tracker{}
	}

	return Tracker{
		client:   tc,
		url:      trackerUrl,
		interval: time.Duration(0),
	}
}

func (t *Tracker) Announce(wg *sync.WaitGroup, ctx context.Context, req AnnounceRequest, respCh chan<- AnnounceResponse, errCh chan<- error) {

	defer wg.Done()

	if t.client == nil {
		errCh <- errors.New("tracker does not implement client")
		return
	}

	resp, err := t.client.Announce(ctx, t.url, req)
	if err != nil {
		errCh <- fmt.Errorf("failed tracker announce: %w", err)
	}

	if resp.interval > 0 {
		t.interval = resp.interval
	} else {
		t.interval = time.Second * 30
	}

	resp.tracker = t

	respCh <- resp
}

func (c *UDPClient) Announce(ctx context.Context, trackerUrl url.URL, req AnnounceRequest) (AnnounceResponse, error) {

	var announceResp AnnounceResponse

	if c.dialer == nil {
		return AnnounceResponse{}, fmt.Errorf("client has no valid dialer")
	}

	serverAddr, err := net.ResolveUDPAddr("udp", trackerUrl.String())
	if err != nil {
		return AnnounceResponse{}, fmt.Errorf("failed to resolve UDP address: %w", err)
	}

	conn, err := c.dialer.DialContext(ctx, "udp", serverAddr.String())
	if err != nil {
		return AnnounceResponse{}, fmt.Errorf("failed to connect to tracker: %w", err)
	}
	defer conn.Close()

	c.mutex.Lock()
	cachedConnId, found := c.connIds[trackerUrl.Host]
	c.mutex.Unlock()

	if found { // IF CONNECTION ID ALREADY EXISTS, SKIP CONNECTION REQUEST
		connId := cachedConnId
		announceResp, err = c.makeAnnounceRequest(ctx, connId, req, conn)
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

	connId, err := c.makeConnectionRequest(ctx, conn)
	if err != nil {
		return AnnounceResponse{}, fmt.Errorf("failed to make connection request: %w", err)
	}

	c.mutex.Lock()
	c.connIds[trackerUrl.Host] = connId
	c.mutex.Unlock()

	announceResp, err = c.makeAnnounceRequest(ctx, connId, req, conn)
	if err != nil {
		return AnnounceResponse{}, fmt.Errorf("failed to make announce request: %w", err)
	}

	return announceResp, nil

}

func (c *HTTPClient) Announce(ctx context.Context, trackerUrl url.URL, req AnnounceRequest) (AnnounceResponse, error) {

	if c.client == nil {
		return AnnounceResponse{}, errors.New("tracker client is nil")
	}

	params := url.Values{}
	params.Set("info_hash", string(req.infoHash[:]))
	params.Set("peer_id", string(req.peerID[:]))
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

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, finalUrl.String(), nil)
	if err != nil {
		return AnnounceResponse{}, fmt.Errorf("failed to create request context: %w", err)
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return AnnounceResponse{}, fmt.Errorf("failed to make tracker requerst: %w", err)
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return AnnounceResponse{}, fmt.Errorf("failed to read tracker response: %w", err)
	}

	parserCtx, err := parser.NewParserContext(b)
	if err != nil {
		return AnnounceResponse{}, fmt.Errorf("parser context creation failed: %w", err)
	}

	rootDict, err := parserCtx.Parse()
	if err != nil {
		return AnnounceResponse{}, fmt.Errorf("response parsing failed: %w", err)
	}

	announceResp, err := parseAnnounceResponse(rootDict)
	if err != nil {
		return AnnounceResponse{}, fmt.Errorf("response parsing failed: %w", err)
	}

	return announceResp, nil
}

func (c *UDPClient) makeAnnounceRequest(ctx context.Context, connId uint64, req AnnounceRequest, conn net.Conn) (AnnounceResponse, error) {

	var announceTransactionIDBytes [4]byte
	if _, err := io.ReadFull(rand.Reader, announceTransactionIDBytes[:]); err != nil {
		return AnnounceResponse{}, fmt.Errorf("failed to generate announce transaction ID: %w", err)
	}
	announceTransactionId := binary.BigEndian.Uint32(announceTransactionIDBytes[:])

	announceMsg := c.generateAnnounceMsg(announceTransactionId, req, connId)

	if deadline, ok := ctx.Deadline(); ok {
		conn.SetWriteDeadline(deadline)
	} else {
		conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
	}

	n, err := conn.Write(announceMsg)
	if err != nil {
		return AnnounceResponse{}, fmt.Errorf("invalid announce response: %w", err)
	}

	if n != 98 {
		return AnnounceResponse{}, fmt.Errorf("wrote %d bytes, expected 98 bytes", n)
	}

	if deadline, ok := ctx.Deadline(); ok {
		conn.SetReadDeadline(deadline)
	} else {
		conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	}

	announceBuffer := make([]byte, 4096)

	// ANNOUNCE READ //
	n, err = conn.Read(announceBuffer)
	if err != nil {
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			return AnnounceResponse{}, fmt.Errorf("UDP connect timed out waiting for response from %s: %w", conn.RemoteAddr().String(), err)
		}
		return AnnounceResponse{}, fmt.Errorf("failed to read UDP connect response: %w", err)
	}

	announceResponseBytes := announceBuffer[:n] // Use actual bytes read

	if len(announceResponseBytes) < 8 { // Need at least action + transactionId for error check
		return AnnounceResponse{}, fmt.Errorf("announce response too short: got %d bytes, expected at least 8", len(announceResponseBytes))
	}

	action := binary.BigEndian.Uint32(announceBuffer[0:4])
	responseTransactionID := binary.BigEndian.Uint32(announceBuffer[4:8])

	if action == uint32(ErrorAction) {
		errorMsg := "tracker returned error"
		if len(announceResponseBytes) > 8 { // Error message is optional
			errorMsg = fmt.Sprintf("tracker returned error: %s", string(announceResponseBytes[8:]))
		}
		return AnnounceResponse{}, errors.New(errorMsg)
	} else if action != uint32(AnnounceAction) {
		return AnnounceResponse{}, fmt.Errorf("expected action announce, got %s", string(announceBuffer[0:4]))
	}

	if n < 20 {
		return AnnounceResponse{}, fmt.Errorf("tracker responded with %d bytes, expected at least 20 bytes: %w", n, err)
	}

	if responseTransactionID != announceTransactionId {
		return AnnounceResponse{}, fmt.Errorf("transaction id from response differs from sent id: expected %d, got %d", responseTransactionID, announceTransactionId)
	}

	peerData := announceBuffer[20:n]
	numPeers := len(peerData) / 6
	if len(peerData)%6 != 0 {
		return AnnounceResponse{}, fmt.Errorf("incomplete peer data received (%d bytes)", len(peerData))
	}

	var announceResp AnnounceResponse
	announceResp.peers = make([]peer.PeerManager, 0, numPeers)
	announceResp.interval = time.Duration(binary.BigEndian.Uint32(announceBuffer[8:12])) * time.Second

	for i := range numPeers {
		offset := i * 6
		ip := net.IP(peerData[offset : offset+4])
		port := binary.BigEndian.Uint16(peerData[offset+4 : offset+6])
		announceResp.peers = append(announceResp.peers, peer.PeerManager{
			PeerIp:   ip,
			PeerPort: port,
		})
	}

	return announceResp, nil
}

func (c *UDPClient) makeConnectionRequest(ctx context.Context, conn net.Conn) (uint64, error) {

	msg := make([]byte, 16) // creates buffer for connect  message

	var connectTransactionId uint32
	binary.BigEndian.PutUint64(msg[0:8], ProtocolID)
	binary.BigEndian.PutUint32(msg[8:12], uint32(ConnectAction))
	binary.BigEndian.PutUint32(msg[12:16], connectTransactionId)

	if deadline, ok := ctx.Deadline(); ok {
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

	if deadline, ok := ctx.Deadline(); ok {
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

	announceMsg := make([]byte, 98)

	binary.BigEndian.PutUint64(announceMsg[0:8], connId)
	binary.BigEndian.PutUint32(announceMsg[8:12], uint32(AnnounceAction))

	binary.BigEndian.PutUint32(announceMsg[12:16], transactionId)
	copy(announceMsg[16:36], req.infoHash[:])
	copy(announceMsg[36:56], req.peerID[:])

	binary.BigEndian.PutUint64(announceMsg[56:64], req.downloaded)
	binary.BigEndian.PutUint64(announceMsg[64:72], req.left)
	binary.BigEndian.PutUint64(announceMsg[72:80], req.uploaded)

	var eventCode uint32 = 0 // Default: none
	switch req.event {
	case "completed":
		eventCode = 1
	case "started":
		eventCode = 2
	case "stopped":
		eventCode = 3
	}

	binary.BigEndian.PutUint64(announceMsg[80:84], uint64(eventCode))

	binary.BigEndian.PutUint32(announceMsg[84:88], 0)
	binary.BigEndian.PutUint32(announceMsg[88:92], 0)
	binary.BigEndian.PutUint32(announceMsg[92:96], 0xFFFFFFFF) // -1 as an uint32
	binary.BigEndian.PutUint16(announceMsg[96:98], req.port)

	return announceMsg
}

func parseAnnounceResponse(root *parser.BencodeValue) (AnnounceResponse, error) {

	var resp AnnounceResponse

	for _, entry := range root.DictValue {
		key, err := entry.Key.GetStringValue()
		if err != nil {
			return AnnounceResponse{}, err
		}

		switch key {

		case "failure":

			val, err := entry.Value.GetStringValue()
			if err != nil {
				return AnnounceResponse{}, fmt.Errorf("error parsing failure reason: %w", err)
			}

			return AnnounceResponse{}, fmt.Errorf("tracker returned failure: %v", val)

		case "interval":

			resp.interval = time.Duration(entry.Value.IntegerValue)
			continue

		case "peers":

			if entry.Value.ValueType == parser.BencodeString {
				const peerSize = 6
				peersListSize := len(root.StringValue)

				if peersListSize%peerSize != 0 {
					return AnnounceResponse{}, fmt.Errorf("invalid compact response format: %w", err)
				}

				for i := 0; i < len(entry.Value.StringValue); i += peerSize {

					ip := net.ParseIP(string(entry.Value.StringValue[i : i+4]))
					port := binary.BigEndian.Uint16(entry.Value.StringValue[i+4 : i+6])

					resp.peers = append(resp.peers, peer.PeerManager{
						PeerIp:   ip,
						PeerPort: port,
					})
				}

			} else {
				for _, peerDict := range entry.Value.ListValue {

					var peer peer.PeerManager
					for _, entry := range peerDict.DictValue {

						k, err := entry.Key.GetStringValue()
						if err != nil {
							return AnnounceResponse{}, err
						}

						switch k {
						case "peer id":
							peer.PeerId = [20]byte(entry.Key.StringValue)
							continue
						case "ip":
							peer.PeerIp = net.ParseIP(string(entry.Value.StringValue))
							continue
						case "port":
							peer.PeerPort = uint16(entry.Value.IntegerValue)
							continue
						}
					}
					resp.peers = append(resp.peers, peer)

				}
			}
		}
	}
	return resp, nil
}
