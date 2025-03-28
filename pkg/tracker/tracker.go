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

	parser "github.com/agaabrieel/bittorrent-client/pkg/parser"
	peer "github.com/agaabrieel/bittorrent-client/pkg/peers"
)

type Action uint8

const ProtocolID uint64 = 0x41727101980

const (
	ConnectAction Action = iota
	AnnounceAction
	ScrapeAction
	ErrorAction
)

type TrackerClient interface {
	Announce(ctx context.Context, trackerUrl url.URL, req AnnounceRequest) (AnnounceResponse, error)
}

type HTTPTrackerClient struct {
	Client *http.Client
}

type UDPTrackerClient struct {
	Dialer  *net.Dialer
	connIds map[string]uint64
	mutex   sync.Mutex
}

type Tracker struct {
	TrackerClient
	Url      url.URL
	Interval time.Duration
	Tier     uint8
	Request  AnnounceRequest
}

type AnnounceRequest struct {
	InfoHash   [20]byte
	PeerID     [20]byte
	Port       uint16
	Uploaded   uint64
	Downloaded uint64
	Left       uint64
	Event      string // "started", "stopped", "completed"
}

type AnnounceResponse struct {
	Interval time.Duration // Seconds to wait between announces
	Peers    []peer.Peer   // List of peers
	// ... other fields
}

func NewAnnounceRequest(hash [20]byte, id [20]byte) *AnnounceRequest {
	req := AnnounceRequest{
		InfoHash: hash,
		PeerID:   id,
	}
	return &req
}

func NewTracker(trackerUrl url.URL, client *http.Client, dialer *net.Dialer, tier ...uint8) *Tracker {

	if client == nil {
		client = &http.Client{
			Timeout: 30 * time.Second,
		}
	}
	if dialer == nil {
		dialer = &net.Dialer{
			Timeout: 30 * time.Second,
		}
	}

	var tc TrackerClient
	if trackerUrl.Scheme == "udp" {
		tc = &UDPTrackerClient{
			Dialer: dialer,
		}
	} else if trackerUrl.Scheme == "http" || trackerUrl.Scheme == "https" {
		tc = &HTTPTrackerClient{
			Client: client,
		}
	} else {
		return nil
	}

	return &Tracker{
		TrackerClient: tc,
		Url:           trackerUrl,
		Interval:      time.Duration(0),
		Tier:          tier[0],
	}
}

func (t *Tracker) Announce(wg *sync.WaitGroup, ctx context.Context, req AnnounceRequest, respCh chan<- AnnounceResponse, errCh chan<- error) {

	defer wg.Done()

	if t.TrackerClient == nil {
		errCh <- errors.New("tracker does not implement client")
		return
	}

	resp, err := t.TrackerClient.Announce(ctx, t.Url, req)
	if err != nil {
		errCh <- fmt.Errorf("failed tracker announce: %w", err)
	}

	if resp.Interval > 0 {
		t.Interval = resp.Interval
	} else {
		t.Interval = time.Second * 30
		errCh <- errors.New("tracker gave 0 interval, resorting to default 30 seconds")
	}
	respCh <- resp
}

func (c *UDPTrackerClient) makeAnnounceRequest(ctx context.Context, connId uint64, req AnnounceRequest, conn net.Conn) (AnnounceResponse, error) {

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
	announceResp.Peers = make([]peer.Peer, 0, numPeers)
	announceResp.Interval = time.Duration(binary.BigEndian.Uint32(announceBuffer[8:12])) * time.Second

	for i := range numPeers {
		offset := i * 6
		ip := net.IP(peerData[offset : offset+4])
		port := binary.BigEndian.Uint16(peerData[offset+4 : offset+6])
		announceResp.Peers = append(announceResp.Peers, peer.Peer{
			Ip:   ip,
			Port: port,
		})
	}

	return announceResp, nil
}

func (c *UDPTrackerClient) makeConnectionRequest(ctx context.Context, conn net.Conn) (uint64, error) {

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

func (c *UDPTrackerClient) generateAnnounceMsg(transactionId uint32, req AnnounceRequest, connId uint64) []byte {

	announceMsg := make([]byte, 98)

	binary.BigEndian.PutUint64(announceMsg[0:8], connId)
	binary.BigEndian.PutUint32(announceMsg[8:12], uint32(AnnounceAction))

	binary.BigEndian.PutUint32(announceMsg[12:16], transactionId)
	copy(announceMsg[16:36], req.InfoHash[:])
	copy(announceMsg[36:56], req.PeerID[:])

	binary.BigEndian.PutUint64(announceMsg[56:64], req.Downloaded)
	binary.BigEndian.PutUint64(announceMsg[64:72], req.Left)
	binary.BigEndian.PutUint64(announceMsg[72:80], req.Uploaded)

	var eventCode uint32 = 0 // Default: none
	switch req.Event {
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
	binary.BigEndian.PutUint16(announceMsg[96:98], req.Port)

	return announceMsg
}

func (c *UDPTrackerClient) Announce(ctx context.Context, trackerUrl url.URL, req AnnounceRequest) (AnnounceResponse, error) {

	var announceResp AnnounceResponse

	if c.Dialer == nil {
		return AnnounceResponse{}, fmt.Errorf("client has no valid dialer")
	}

	serverAddr, err := net.ResolveUDPAddr("udp", trackerUrl.String())
	if err != nil {
		return AnnounceResponse{}, fmt.Errorf("failed to resolve UDP address: %w", err)
	}

	conn, err := c.Dialer.DialContext(ctx, "udp", serverAddr.String())
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

func (c *HTTPTrackerClient) Announce(ctx context.Context, trackerUrl url.URL, req AnnounceRequest) (AnnounceResponse, error) {

	if c.Client == nil {
		return AnnounceResponse{}, errors.New("tracker client is nil")
	}

	params := url.Values{}
	params.Set("info_hash", string(req.InfoHash[:]))
	params.Set("peer_id", string(req.PeerID[:]))
	params.Set("port", strconv.Itoa(int(req.Port)))

	uploaded := strconv.Itoa(int(req.Uploaded))
	params.Set("uploaded", uploaded)

	dowloaded := strconv.Itoa(int(req.Downloaded))
	params.Set("downloaded", dowloaded)

	left := strconv.Itoa(int(req.Left))
	params.Set("left", left)

	params.Set("compact", string("1"))
	if req.Event != "" {
		params.Set("event", req.Event)
	}

	finalUrl := trackerUrl
	finalUrl.RawQuery = params.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, finalUrl.String(), nil)
	if err != nil {
		return AnnounceResponse{}, fmt.Errorf("failed to create request context: %w", err)
	}

	resp, err := c.Client.Do(httpReq)
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

			resp.Interval = time.Duration(entry.Value.IntegerValue)
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

					resp.Peers = append(resp.Peers, peer.Peer{
						Ip:   ip,
						Port: port,
					})
				}

			} else {
				for _, peerDict := range entry.Value.ListValue {

					var peer peer.Peer
					for _, entry := range peerDict.DictValue {

						k, err := entry.Key.GetStringValue()
						if err != nil {
							return AnnounceResponse{}, err
						}

						switch k {
						case "peer id":
							peer.Id = [20]byte(entry.Key.StringValue)
							continue
						case "ip":
							peer.Ip = net.ParseIP(string(entry.Value.StringValue))
							continue
						case "port":
							peer.Port = uint16(entry.Value.IntegerValue)
							continue
						}
					}
					resp.Peers = append(resp.Peers, peer)

				}
			}
		}
	}
	return resp, nil
}
