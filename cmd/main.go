package main

import (
	"os"

	"github.com/agaabrieel/bittorrent-client/pkg/messaging"
	session "github.com/agaabrieel/bittorrent-client/pkg/torrent"
)

func main() {

	Router := messaging.NewRouter()
	TorrentSession, err := session.NewSessionManager(os.Args[1], Router)
	if err != nil {
		os.Exit(1)
	}

	TorrentSession.Run()
}
