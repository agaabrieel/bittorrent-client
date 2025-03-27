package client

import (
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

	metainfo "github.com/agaabrieel/bittorrent-client/pkg/metainfo"
	parser "github.com/agaabrieel/bittorrent-client/pkg/parser"
	peer "github.com/agaabrieel/bittorrent-client/pkg/peers"
	tracker "github.com/agaabrieel/bittorrent-client/pkg/tracker"
)

type PieceManager struct {
}

type BitTorrentClient struct {
	id           [20]byte
	port         int
	Torrent      *metainfo.TorrentMetainfo
	Peers        []peer.Peer
	PieceManager *PieceManager
	Trackers     []tracker.Tracker
}

func NewBittorrentClient(port int, t *metainfo.TorrentMetainfo) (*BitTorrentClient, error) {

	var trackers []tracker.Tracker

	if len(t.AnnounceList) != 0 {
		for tierVal, tier := range t.AnnounceList {
			for _, tracker := range tier {
				baseUrl, err := url.Parse(tracker)
				if err != nil {
					return nil, err
				}
				trackers = append(trackers, tracker.Tracker{
					url:      *baseUrl,
					tier:     uint8(tierVal),
					interval: time.Duration(0),
				})
			}
		}
	}

	baseUrl, err := url.Parse(t.Announce)
	if err != nil {
		return nil, err
	}

	tracker := Tracker{
		url:      *baseUrl,
		tier:     uint8(0),
		interval: time.Duration(0),
	}

	trackers = append(trackers, tracker)

	c := BitTorrentClient{
		id:       getNewId(),
		Trackers: trackers,
		Torrent:  t,
		port:     port,
	}

	return &c, nil
}

func getNewId() [20]byte {
	var b [20]byte
	rand.Read(b[:])
	return b
}

func (c *BitTorrentClient) BuildTrackersQueries() error {

	params := url.Values{
		"info_hash":  []string{string(c.Torrent.Infohash[:])},
		"peer_id":    []string{string(c.id[:])},
		"port":       []string{strconv.Itoa(c.port)},
		"uploaded":   []string{"0"},
		"downloaded": []string{"0"},
		"compact":    []string{"1"},
		"left":       []string{strconv.Itoa(int(c.Torrent.InfoDict.Length))},
	}

	for _, tracker := range c.Trackers {
		tracker.url.RawQuery = params.Encode()
	}

	return nil
}

func (c *BitTorrentClient) FetchPeers() error {

	var wg sync.WaitGroup

	peerCh := make(chan Peer, 100)
	errCh := make(chan error, len(c.Trackers))

	for _, tracker := range c.Trackers {
		wg.Add(1)
		go fetchPeers(&wg, tracker, peerCh, errCh)
	}

	wg.Wait()
	close(peerCh)
	close(errCh)

	peerSet := make(map[Peer]bool)
	for peer := range peerCh {
		if peerSet[peer] {
			continue
		}
		peerSet[peer] = true
		c.Peers = append(c.Peers, peer)
	}

	err := errors.Join(<-errCh)

	return err
}

func fetchPeers(wg *sync.WaitGroup, tracker Tracker, peerCh chan<- Peer, errCh chan<- error) {

	defer wg.Done()

	var b []byte
	var err error
	if tracker.url.Scheme == "udp" {

		resp, err := net.Dial(tracker.url.Scheme, tracker.url.String())
		if err != nil {
			errCh <- fmt.Errorf("tracker %s failed: %v", tracker.url.String(), err)
			return
		}
		defer resp.Close()

		b, err = io.ReadAll(resp)
		if err != nil {
			errCh <- fmt.Errorf("failed to read response from %s: %v", tracker.url.String(), err)
			return
		}

	} else {
		resp, err := http.Get(tracker.url.RawQuery)
		if err != nil {
			errCh <- fmt.Errorf("tracker %s failed: %v", tracker.url.String(), err)
			return
		}
		defer resp.Body.Close()

		b, err = io.ReadAll(resp.Body)
		if err != nil {
			errCh <- fmt.Errorf("failed to read response from %s: %v", tracker.url.String(), err)
			return
		}

	}

	ctx, err := parser.NewParserContext(b)
	if err != nil {
		errCh <- fmt.Errorf("failed to created parsing context from response: %v", err)
		return
	}

	bencodeRootDict, err := ctx.Parse()
	if err != nil {
		errCh <- err
		return
	}

	for _, entry := range bencodeRootDict.DictValue {
		key, err := entry.Key.GetStringValue()
		if err != nil {
			errCh <- fmt.Errorf("failed to get string value from key %s: %v", key, err)
			continue
		}

		switch key {

		case "failure":

			val, err := entry.Value.GetStringValue()
			if err != nil {
				errCh <- fmt.Errorf("peer failed to send valid response: %v", err)
				return
			}
			errCh <- errors.New(val)
			return

		case "interval":

			tracker.interval = time.Duration(entry.Value.IntegerValue)
			continue

		case "peers":

			if entry.Value.ValueType == parser.BencodeString {
				const peerSize = 6
				peersListSize := len(bencodeRootDict.StringValue)

				if peersListSize%peerSize != 0 {
					errCh <- fmt.Errorf("invalid compact peer format: %d", peersListSize)
					return
				}

				var peer Peer
				for i := 0; i < len(bencodeRootDict.StringValue); i += peerSize {

					peer.ip = string(bencodeRootDict.StringValue[i : i+4])
					peer.port = binary.BigEndian.Uint16(bencodeRootDict.StringValue[i+4 : i+6])

					select {
					case peerCh <- peer:
					default:
						errCh <- errors.New("peer channel is full")
						return
					}
				}

			} else {
				for _, peerDict := range entry.Value.ListValue {

					var peer Peer
					for _, entry := range peerDict.DictValue {

						k, err := entry.Key.GetStringValue()
						if err != nil {
							errCh <- fmt.Errorf("failed to get key %s: %v", string(entry.Key.StringValue), err)
							continue
						}

						switch k {
						case "peer id":
							copy(peer.id[:], entry.Value.StringValue)
							continue
						case "ip":
							peer.ip = string(entry.Value.StringValue)
							continue
						case "port":
							peer.port = uint16(entry.Value.IntegerValue)
							continue
						}
					}

					select {
					case peerCh <- peer:
					default:
						errCh <- fmt.Errorf("peer channel is full")
						return
					}
				}
			}
		}
	}
}
