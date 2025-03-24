package main

import (
	parser "bencode_parser/internal"
	"fmt"
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
		return
	}
	println(bencode_str)
}
