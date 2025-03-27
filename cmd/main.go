package main

import (
	"fmt"

	client "github.com/agaabrieel/bittorrent-client/pkg/client"
	torrent "github.com/agaabrieel/bittorrent-client/pkg/torrent"
)

func main() {

	torrent := torrent.NewTorrent()

	err := torrent.Deserialize("test.torrent")
	if err != nil {
		fmt.Println(err)
		return
	}

	c, err := client.NewBittorrentClient(6881, torrent)
	if err != nil {
		fmt.Println(err)
	}

	err = c.BuildTrackersQueries()
	if err != nil {
		fmt.Println(err)
	}

	err = c.FetchPeers()
	if err != nil {
		fmt.Println(err)
	}

	fmt.Printf("%x %v\n", torrent.Infohash, len(torrent.Infohash))
}
