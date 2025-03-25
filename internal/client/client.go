package client

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"

	parser "github.com/agaabrieel/bittorrent-client/internal/parser"
	torrent "github.com/agaabrieel/bittorrent-client/internal/torrent"
)

type BittorrentClient struct {
	http.Client
	id       [20]byte
	port     uint16
	interval uint32
	peers    []Peer
}

func generateNewId() [20]byte {
	b := make([]byte, 20)
	rand.Read(b)
	return [20]byte(b)
}

func NewBittorrentClient(port uint16) BittorrentClient {
	c := BittorrentClient{
		port:   port,
		id:     generateNewId(),
		Client: http.Client{},
	}
	return c
}

func (c *BittorrentClient) BuildTrackersUrls(t *torrent.Torrent) (map[int][]url.URL, error) {

	params := url.Values{
		"info_hash":  []string{string(t.Infohash[:])},
		"peer_id":    []string{string(c.id[:])},
		"port":       []string{strconv.Itoa(int(c.port))},
		"uploaded":   []string{"0"},
		"downloaded": []string{"0"},
		"compact":    []string{"1"},
		"left":       []string{strconv.Itoa(int(t.InfoDict.Length))},
	}

	trackers := make(map[int][]url.URL, 100)
	if len(t.AnnounceList) != 0 {
		for tierVal, tier := range t.AnnounceList {
			for _, urlString := range tier {
				url, err := url.Parse(urlString)
				if err != nil {
					return nil, err
				}
				url.RawQuery = params.Encode()
				trackers[tierVal] = append(trackers[tierVal], *url)
			}
		}
	} else {
		baseUrl, err := url.Parse(t.Announce)
		if err != nil {
			return nil, err
		}
		baseUrl.RawQuery = params.Encode()
		trackers[0] = append(trackers[0], *baseUrl)
	}
	return trackers, nil
}

func (c *BittorrentClient) fetchTrackerForPeers(wg *sync.WaitGroup, url url.URL, peerCh chan<- Peer, intervalCh chan<- uint32, errCh chan<- error) {

	defer wg.Done()

	resp, err := c.Client.Get(url.String())
	if err != nil {
		errCh <- fmt.Errorf("tracker %s failed: %v", url.String(), err)
		return
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		errCh <- fmt.Errorf("failed to read response from %s: %v", url.String(), err)
		return
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

	if bencodeRootDict.ValueType == parser.BencodeString {
		const peerSize = 6
		peersListSize := len(bencodeRootDict.StringValue)

		if peersListSize%peerSize != 0 {
			errCh <- fmt.Errorf("invalid compact peer format")
			return
		}

		var peer Peer
		for i := 0; i < len(bencodeRootDict.StringValue); i += peerSize {
			peer.ip = string(bencodeRootDict.StringValue[i : i+4])
			peer.port = binary.BigEndian.Uint16(bencodeRootDict.StringValue[i+4 : i+6])
			peerCh <- peer
		}

	} else {

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
				select {
				case intervalCh <- uint32(entry.Value.IntegerValue):
				default:
					errCh <- fmt.Errorf("interval ch already full")
					continue
				}
			case "peers":

				var peer Peer
				for _, peerDict := range entry.Value.DictValue {

					k, err := peerDict.Key.GetStringValue()
					if err != nil {
						errCh <- fmt.Errorf("failed to get key %s: %v", string(peerDict.Key.StringValue), err)
						continue
					}

					switch k {
					case "peer id":
						copy(peer.id[:], entry.Value.StringValue)
					case "ip":
						peer.ip = string(entry.Value.StringValue)
					case "port":
						peer.port = uint16(entry.Value.IntegerValue)
					}
					peerCh <- peer
				}

			}
		}
	}
}

func (c *BittorrentClient) BuildPeersInfo(t *torrent.Torrent) error {

	var wg sync.WaitGroup

	trackers, err := c.BuildTrackersUrls(t)
	if err != nil {
		return err
	}

	interCh := make(chan uint32)
	peerCh := make(chan Peer, 100)
	errCh := make(chan error, len(trackers))

	for _, tier := range trackers {
		for _, url := range tier {
			wg.Add(1)
			go c.fetchTrackerForPeers(&wg, url, peerCh, interCh, errCh)
		}
	}

	go func() {
		wg.Wait()
		close(interCh)
		close(peerCh)
		close(errCh)
	}()

	c.interval = <-interCh

	for peer := range peerCh {
		c.peers = append(c.peers, peer)
	}

	err = errors.Join(<-errCh)

	return err
}
