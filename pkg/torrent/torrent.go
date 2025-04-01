package session

import (
	"crypto/rand"
	"fmt"
	"net/url"
	"sync"

	metainfo "github.com/agaabrieel/bittorrent-client/pkg/metainfo"
	peer "github.com/agaabrieel/bittorrent-client/pkg/peers"
	piece "github.com/agaabrieel/bittorrent-client/pkg/piece"
	tracker "github.com/agaabrieel/bittorrent-client/pkg/tracker"
)

type ChannelMessage uint8

const (
	GetBitfield ChannelMessage = iota
)

type TorrentSession struct {
	Id           [20]byte
	Metainfo     *metainfo.TorrentMetainfo
	Trackers     []tracker.Tracker
	PeerManager  *peer.PeerManager
	PieceManager *piece.PieceManager
	WaitGroup    *sync.WaitGroup
	Mutex        *sync.Mutex
}

func NewTorrentSession(filepath string) (*TorrentSession, error) {

	var t TorrentSession

	meta := metainfo.NewTorrent()
	if err := meta.Deserialize(filepath); err != nil {
		return nil, fmt.Errorf("failed to parse torrent file: %w", err)
	}

	for _, tier := range meta.AnnounceList {
		for _, baseUrl := range tier {
			parsedUrl, err := url.Parse(baseUrl)
			if err != nil {
				continue
			}

			tracker := tracker.NewTracker(*parsedUrl)
			t.Trackers = append(t.Trackers, tracker)
		}
	}

	rand.Read(t.Id[:])
	t.Metainfo = meta
	t.Mutex = &sync.Mutex{}
	t.WaitGroup = &sync.WaitGroup{}

	return &t, nil

}

func (t *TorrentSession) Init() {

	responseCh := make(chan tracker.AnnounceResponse, 10)
	errCh := make(chan error, 10)
}
