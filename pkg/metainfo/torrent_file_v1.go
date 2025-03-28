package metainfo

import (
	"crypto/sha1"
	"fmt"

	parser "github.com/agaabrieel/bittorrent-client/pkg/parser"
)

type TorrentMetainfo struct {
	Announce     string
	AnnounceList [][]string
	CreationDate int64
	Author       string
	InfoDict     *TorrentMetainfoInfoDict
	Extra        []parser.BencodeValue
	Infohash     [sha1.Size]byte
}

type TorrentMetainfoInfoDict struct {
	Length      uint64
	Name        string
	Files       []TorrentMetainfoFilesDict
	PieceLength uint64
	Pieces      []byte
	Extra       []parser.BencodeValue
}

type TorrentMetainfoFilesDict struct {
	Len  uint64
	Path string
}

func NewMetainfo() *TorrentMetainfo {
	var t TorrentMetainfo
	return &t
}

func (t *TorrentMetainfo) Deserialize(filepath string) error {

	extra := make([]parser.BencodeValue, 5)

	data, err := parser.ReadTorrentFile(filepath)
	if err != nil {
		return err
	}

	ctx, err := parser.NewParserContext(data)
	if err != nil {
		return err
	}

	rootBencodeDict, err := ctx.Parse()
	if err != nil {
		return err
	}

	dict := rootBencodeDict.DictValue
	for _, entry := range dict {

		if entry.Key.ValueType != parser.BencodeString {
			continue
		}

		keyValue, err := entry.Key.GetStringValue()
		if err != nil {
			return fmt.Errorf("invalid dictionary key: %w", err)
		}

		switch keyValue {

		case "announce":

			announce, err := entry.Value.GetStringValue()
			if err != nil {
				return fmt.Errorf("invalid announce value: %w", err)
			}

			t.Announce = announce

		case "announce-list":

			tierlists := entry.Value.ListValue
			for _, tierList := range tierlists {

				tier := tierList.ListValue
				urls := make([]string, len(tier))
				for _, urlValue := range tier {

					url, err := urlValue.GetStringValue()
					if err != nil {
						return fmt.Errorf("invalid url: %w", err)
					}
					urls = append(urls, string(url))
				}

				t.AnnounceList = append(t.AnnounceList, urls)
			}

		case "info":
			t.deserializeInfoDict(entry)
		case "creation_date":
			t.CreationDate = entry.Value.IntegerValue
		case "created by":

			stringValue, err := entry.Value.GetStringValue()
			if err != nil {
				return fmt.Errorf("invalid author name: %w", err)
			}
			t.Author = stringValue

		default:
			extra = append(extra, *entry.Value)
		}
	}

	t.Extra = extra

	return nil
}

func (t *TorrentMetainfo) deserializeInfoDict(entry parser.BencodeDictEntry) error {

	rawInfo, err := entry.Value.Serialize()
	if err != nil {
		return fmt.Errorf("error parsing infohash: %w", err)
	}

	var info TorrentMetainfoInfoDict
	infoDict := entry.Value.DictValue
	for _, entry := range infoDict {

		kType := entry.Key.ValueType
		if kType != parser.BencodeString {
			continue
		}

		kValue, err := entry.Key.GetStringValue()
		if err != nil {
			return fmt.Errorf("invalid info dict: %w", err)
		}

		switch kValue {

		case "length":

			length := entry.Value.IntegerValue
			info.Length = uint64(length)

		case "name":

			name, err := entry.Value.GetStringValue()
			if err != nil {
				return fmt.Errorf("invalid name: %w", err)
			}
			info.Name = name

		case "piece length":

			length := entry.Value.IntegerValue
			info.PieceLength = uint64(length)

		case "pieces":

			pieces := entry.Value.StringValue
			info.Pieces = pieces

		case "files":

			files := entry.Value.ListValue
			for _, fileDict := range files {
				file := TorrentMetainfoFilesDict{
					Len:  uint64(fileDict.DictValue[0].Value.IntegerValue),
					Path: string(fileDict.DictValue[0].Value.StringValue),
				}
				info.Files = append(info.Files, file)
			}

		default:
			info.Extra = append(info.Extra, *entry.Value)
		}

	}

	t.setInfoHash(rawInfo)
	t.InfoDict = &info

	return nil
}

func (t *TorrentMetainfo) setInfoHash(raw []byte) {
	hash := sha1.New()
	t.Infohash = [20]byte(hash.Sum(raw))
}
