package parser

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
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

func (bencodeValue *BencodeValue) Marshal() ([]byte, error) {
	var buf bytes.Buffer
	if err := bencodeValue.marshal(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (bencodeValue *BencodeValue) marshal(w io.Writer) error {
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
			if err := (&item).marshal(w); err != nil {
				return err
			}
		}
		w.Write([]byte{'e'})
	case BencodeDict:
		w.Write([]byte{'d'})
		for _, entry := range bencodeValue.DictValue {
			if err := entry.Key.marshal(w); err != nil {
				return err
			}
			if err := entry.Value.marshal(w); err != nil {
				return err
			}
		}
		w.Write([]byte{'e'})
	}
	return nil
}
func (bencodeValue *BencodeValue) GetType() BencodeType {
	return bencodeValue.ValueType
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

func (v *BencodeValue) Stringify(indent ...uint8) (string, error) {

	var tab uint8
	if indent == nil {
		tab = 0
	} else {
		tab = indent[0]
	}

	var ret string
	switch v.ValueType {
	case BencodeInteger:
		ret += strings.Repeat(" ", int(tab)) + "i" + strconv.Itoa(int(v.IntegerValue)) + "e\n"
	case BencodeString:
		ret += strings.Repeat(" ", int(tab)) + strconv.Itoa(len(v.StringValue)) + ":" + string(v.StringValue) + "\n"
	case BencodeDict:
		ret += strings.Repeat(" ", int(tab)) + "d\n"
		tab++
		for _, entry := range v.DictValue {

			str, err := entry.Key.Stringify(tab)
			if err != nil {
				return "", fmt.Errorf("failed to print bencode value: %w", err)
			}
			ret += str

			str, err = entry.Value.Stringify(tab)
			if err != nil {
				return "", fmt.Errorf("failed to print bencode value: %w", err)
			}
			ret += str
		}
		tab--
		ret += strings.Repeat(" ", int(tab)) + "e\n"
	case BencodeList:
		ret += strings.Repeat(" ", int(tab)) + "l\n\t"
		for _, entry := range v.ListValue {
			str, err := (&entry).Stringify(tab)
			if err != nil {
				return "", fmt.Errorf("failed to print bencode value: %w", err)
			}
			ret += strings.Repeat(" ", int(tab)) + str
		}
		ret += strings.Repeat(" ", int(tab)) + "e\n"
	default:
		return "", errors.New("failed to print bencode value")
	}
	return ret, nil
}
