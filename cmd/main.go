package main

import (
	"fmt"

	parser "github.com/agaabrieel/bittorrent-client/internal/parser"
	torrent "github.com/agaabrieel/bittorrent-client/internal/torrent"
)

func main() {
	ctx, err := parser.NewParserContext("test.torrent")
	if err != nil {
		fmt.Println(err)
		return
	}

	root, err := parser.ParseBencode(ctx)
	if err != nil {
		fmt.Println(err)
		return
	}

	bencode_str, err := parser.StringifyBencode(root)
	if err != nil {
		fmt.Println(err)
		return
	}

	torrent := torrent.NewTorrent()

	println(bencode_str)

	fmt.Printf("%x %v\n", torrent.Infohash, len(torrent.Infohash))
}
