package client

import (
	"net/url"
	"strconv"

	torrent "github.com/agaabrieel/bittorrent-client/internal/torrent"
)

func BuildTrackerUrl(t *torrent.Torrent, peerID [20]byte, port uint16) (string, error) {
	baseUrl, err := url.Parse(t.Announce)
	if err != nil {
		return "", err
	}
	params := url.Values{
		"info_hash":  []string{string(t.Infohash[:])},
		"peer_id":    []string{string(peerID[:])},
		"port":       []string{strconv.Itoa(int(port))},
		"uploaded":   []string{"0"},
		"downloaded": []string{"0"},
		"compact":    []string{"1"},
		"left":       []string{strconv.Itoa(int(t.InfoDict.Length))},
	}
	baseUrl.RawQuery = params.Encode()
	return baseUrl.String(), nil
}
