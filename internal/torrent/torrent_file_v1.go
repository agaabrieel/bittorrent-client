package torrent

import (
	"crypto/sha1"
	"fmt"

	parser "github.com/agaabrieel/bittorrent-client/internal/parser"
)

type Torrent struct {
	Announce     string
	AnnounceList [][]string
	CreationDate int64
	Author       string
	InfoDict     *TorrentInfoDict
	Extra        []parser.BencodeValue
	Infohash     [sha1.Size]byte
}

type TorrentInfoDict struct {
	Length      uint64
	Name        string
	Files       []TorrentFilesDict
	PieceLength uint64
	Pieces      []byte
	Extra       []parser.BencodeValue
}

type TorrentFilesDict struct {
	Len  uint64
	Path string
}

func NewTorrent() *Torrent {
	var t Torrent
	return &t
}

func (t *Torrent) Deserialize(filepath string) error {

	var torrent Torrent
	extra := make([]parser.BencodeValue, 0)

	ctx, err := parser.NewParserContext(filepath)
	if err != nil {
		return err
	}

	rootBencodeDict, err := ctx.Parse()
	if err != nil {
		return err
	}

	dict, err := rootBencodeDict.GetDictValue()
	if err != nil {
		return fmt.Errorf("invalid torrent structure: %w", err)
	}

	for _, entry := range dict {

		if entry.Key.ValueType != parser.BencodeString {
			continue
		}

		keyValue, err := entry.Key.GetStringValue()
		if err != nil {
			return fmt.Errorf("invalid dictionary key: %w", err)
		}

		switch string(keyValue) {

		case "announce":

			announce, err := entry.Value.GetStringValue()
			if err != nil {
				return fmt.Errorf("invalid announce value: %w", err)
			}

			torrent.Announce = string(announce)

		case "announce-list":

			tierlists, err := entry.Value.GetListValue()
			if err != nil {
				return fmt.Errorf("invalid announce list: %w", err)
			}

			for _, tierList := range tierlists {

				tier, err := tierList.GetListValue()
				if err != nil {
					return fmt.Errorf("invalid announce tier: %w", err)
				}

				urls := make([]string, len(tier))
				for _, urlValue := range tier {

					url, err := urlValue.GetStringValue()
					if err != nil {
						return fmt.Errorf("invalid url: %w", err)
					}
					urls = append(urls, string(url))
				}

				torrent.AnnounceList = append(torrent.AnnounceList, urls)
			}

		case "info":

			rawInfo, err := entry.Value.Serialize()
			if err != nil {
				return fmt.Errorf("error parsing infohash: %w", err)
			}

			var info TorrentInfoDict

			infoDict, err := entry.Value.GetDictValue()
			if err != nil {
				return fmt.Errorf("invalid info dict: %w", err)
			}

			for _, entry := range infoDict {

				kType := entry.Key.ValueType
				if kType != parser.BencodeString {
					continue
				}

				kValue, err := entry.Key.GetStringValue()
				if err != nil {
					return fmt.Errorf("invalid info dict: %w", err)
				}

				switch string(kValue) {

				case "length":

					length, err := entry.Value.GetIntegerValue()
					if err != nil {
						return fmt.Errorf("invalid length: %w", err)
					}
					info.Length = uint64(length)

				case "name":

					name, err := entry.Value.GetStringValue()
					if err != nil {
						return fmt.Errorf("invalid name: %w", err)
					}
					info.Name = string(name)

				case "piece length":

					length, err := entry.Value.GetIntegerValue()
					if err != nil {
						return fmt.Errorf("invalid length: %w", err)
					}
					info.PieceLength = uint64(length)

				case "pieces":

					pieces, err := entry.Value.GetStringValue()
					if err != nil {
						return fmt.Errorf("invalid pieces: %w", err)
					}
					info.Pieces = pieces

				case "files":

					files, err := entry.Value.GetListValue()
					if err != nil {
						return fmt.Errorf("invalid files: %w", err)
					}

					for _, fileDict := range files {
						file := TorrentFilesDict{
							Len:  uint64(fileDict.DictValue[0].Value.IntegerValue),
							Path: string(fileDict.DictValue[0].Value.StringValue),
						}
						info.Files = append(info.Files, file)
					}

				default:
					info.Extra = append(info.Extra, *entry.Value)
				}

			}

			torrent.SetInfoHash(rawInfo)
			torrent.InfoDict = &info

		case "creation_date":

			integerValue, err := entry.Value.GetIntegerValue()
			if err != nil {
				return fmt.Errorf("invalid creation date: %w", err)
			}
			torrent.CreationDate = integerValue

		case "created by":

			stringValue, err := entry.Value.GetStringValue()
			if err != nil {
				return fmt.Errorf("invalid author name: %w", err)
			}
			torrent.Author = string(stringValue)

		default:
			extra = append(extra, *entry.Value)
		}
	}

	torrent.Extra = extra

	return nil
}

// func parseInfoDict(bencodeValue *parser.BencodeValue) (*TorrentInfoDict, error) {

// 	var info TorrentInfoDict

// 	infoDict, err := bencodeValue.GetDictValue()
// 	if err != nil {
// 		return nil, fmt.Errorf("invalid info dict: %w", err)
// 	}

// 	for _, entry := range infoDict {

// 		kType := entry.Key.ValueType
// 		if kType != parser.BencodeString {
// 			continue
// 		}

// 		kValue, err := entry.Key.GetStringValue()
// 		if err != nil {
// 			return nil, fmt.Errorf("invalid info dict: %w", err)
// 		}

// 		switch string(kValue) {

// 		case "length":

// 			length, err := entry.Value.GetIntegerValue()
// 			if err != nil {
// 				return nil, fmt.Errorf("invalid length: %w", err)
// 			}
// 			info.Length = uint64(length)

// 		case "name":

// 			name, err := entry.Value.GetStringValue()
// 			if err != nil {
// 				return nil, fmt.Errorf("invalid name: %w", err)
// 			}
// 			info.Name = string(name)

// 		case "piece length":

// 			length, err := entry.Value.GetIntegerValue()
// 			if err != nil {
// 				return nil, fmt.Errorf("invalid length: %w", err)
// 			}
// 			info.PieceLength = uint64(length)

// 		case "pieces":

// 			pieces, err := entry.Value.GetStringValue()
// 			if err != nil {
// 				return nil, fmt.Errorf("invalid pieces: %w", err)
// 			}
// 			info.Pieces = pieces

// 		case "files":

// 			files, err := entry.Value.GetListValue()
// 			if err != nil {
// 				return nil, fmt.Errorf("invalid files: %w", err)
// 			}

// 			for _, fileDict := range files {
// 				file := TorrentFilesDict{
// 					Len:  uint64(fileDict.DictValue[0].Value.IntegerValue),
// 					Path: string(fileDict.DictValue[0].Value.StringValue),
// 				}
// 				info.Files = append(info.Files, file)
// 			}

// 		default:
// 			info.Extra = append(info.Extra, *entry.Value)
// 		}

// 	}

// 	return &info, nil

// }

func (t *Torrent) SetInfoHash(raw []byte) {
	hash := sha1.New()
	t.Infohash = [20]byte(hash.Sum(raw))
}
