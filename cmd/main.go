package main

import (
	"fmt"

	torrent "github.com/agaabrieel/bittorrent-client/internal/torrent"
)

func main() {

	torrent := torrent.NewTorrent()
	err := torrent.Deserialize("test.torrent")
	if err != nil {
		fmt.Println(err)
		return
	}

	fmt.Printf("%x %v\n", torrent.Infohash, len(torrent.Infohash))
}
