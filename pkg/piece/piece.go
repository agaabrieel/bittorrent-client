package piece

import "github.com/agaabrieel/bittorrent-client/pkg/metainfo"

type PieceManager struct {
	Metainfo *metainfo.TorrentMetainfoInfoDict
}

type Piece struct {
	Index uint32
	Begin uint32
	Data  []byte
}
