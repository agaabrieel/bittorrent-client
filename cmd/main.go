package main

import (
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"

	"github.com/agaabrieel/bittorrent-client/pkg/messaging"
	session "github.com/agaabrieel/bittorrent-client/pkg/torrent"
)

func main() {

	go func() {
		log.Println(http.ListenAndServe("localhost:6060", nil))
	}()

	Router := messaging.NewRouter()
	TorrentSession, err := session.NewSessionManager(os.Args[1], Router)
	if err != nil {
		os.Exit(1)
	}

	TorrentSession.Run()
}
