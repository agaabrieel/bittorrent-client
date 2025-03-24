package parser

import (
	"bytes"
	"errors"
	"fmt"
	"io"
)

type BencodeType uint8

const (
	BencodeString BencodeType = iota
	BencodeInteger
	BencodeList
	BencodeDict
)

type BencodeDictEntry struct {
	Key   *BencodeValue
	Value *BencodeValue
}

type BencodeValue struct {
	ValueType    BencodeType
	IntegerValue int64
	ListValue    []BencodeValue
	StringValue  []byte
	DictValue    []BencodeDictEntry
}

type ParserContext struct {
	input []byte
	size  uint64
	pos   uint64
}

func (bencodeValue *BencodeValue) Serialize() ([]byte, error) {
	var buf bytes.Buffer
	if err := bencodeValue.serialize(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (bencodeValue *BencodeValue) serialize(w io.Writer) error {
	switch bencodeValue.ValueType {
	case BencodeString:
		_, err := fmt.Fprintf(w, "%d:%s", len(bencodeValue.StringValue), bencodeValue.StringValue)
		return err
	case BencodeInteger:
		_, err := fmt.Fprintf(w, "i%de", bencodeValue.IntegerValue)
		return err
	case BencodeList:
		w.Write([]byte{'l'})
		for _, item := range bencodeValue.ListValue {
			if err := (&item).serialize(w); err != nil {
				return err
			}
		}
		w.Write([]byte{'e'})
	case BencodeDict:
		w.Write([]byte{'d'})
		for _, entry := range bencodeValue.DictValue {
			if err := entry.Key.serialize(w); err != nil {
				return err
			}
			if err := entry.Value.serialize(w); err != nil {
				return err
			}
		}
		w.Write([]byte{'e'})
	}
	return nil
}

func (bencodeValue *BencodeValue) GetStringValue() ([]byte, error) {
	if bencodeValue.ValueType == BencodeString {
		return bencodeValue.StringValue, nil
	} else {
		return nil, errors.New("bencode ValueType property is not BencodeString")
	}
}

func (bencodeValue *BencodeValue) GetIntegerValue() (int64, error) {
	if bencodeValue.ValueType == BencodeInteger {
		return bencodeValue.IntegerValue, nil
	} else {
		return 0, errors.New("bencode ValueType property is not BencodeInteger")
	}
}

func (bencodeValue *BencodeValue) GetDictValue() ([]BencodeDictEntry, error) {
	if bencodeValue.ValueType == BencodeDict {
		return bencodeValue.DictValue, nil
	} else {
		return nil, errors.New("bencode ValueType property is not BencodeDict")
	}
}

func (bencodeValue *BencodeValue) GetListValue() ([]BencodeValue, error) {
	if bencodeValue.ValueType == BencodeList {
		return bencodeValue.ListValue, nil
	} else {
		return nil, errors.New("bencode ValueType property is not BencodeList")
	}
}
