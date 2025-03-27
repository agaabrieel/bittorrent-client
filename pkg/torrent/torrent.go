package torrent

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/url"
	"sync"

	metainfo "github.com/agaabrieel/bittorrent-client/pkg/metainfo"
	peer "github.com/agaabrieel/bittorrent-client/pkg/peers"
	piece "github.com/agaabrieel/bittorrent-client/pkg/piece"
	tracker "github.com/agaabrieel/bittorrent-client/pkg/tracker"
)

type Torrent struct {
	Metainfo     *metainfo.TorrentMetainfo
	Trackers     []tracker.Tracker
	PeerManager  *peer.PeerManager
	PieceManager *piece.PieceManager
}

func NewTorrent(filepath string) (*Torrent, error) {

	var t Torrent

	meta := metainfo.NewTorrent()
	err := meta.Deserialize(filepath)

	if err != nil {
		return nil, fmt.Errorf("failed to parse torrent file: %w", err)
	}

	for _, tier := range meta.AnnounceList {
		for _, baseUrl := range tier {
			url, err := url.Parse(baseUrl)
			if err != nil {
				continue
			}

			tracker := tracker.NewTracker(*url, nil, nil)
			t.Trackers = append(t.Trackers, *tracker)
		}
	}

	id := make([]byte, 20)
	rand.Read(id)

	req := tracker.NewAnnounceRequest(meta.Infohash, id)

	errCh := make(chan<- error, 100)
	respCh := make(chan<- tracker.AnnounceResponse, 100)
	var wg sync.WaitGroup
	for _, tracker := range t.Trackers {
		wg.Add(1)
		go tracker.Announce(&wg, context.Background(), *req, respCh, errCh)
	}

	wg.Wait()
}
