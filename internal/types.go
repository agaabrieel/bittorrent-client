package parser

import (
	"errors"
	"strconv"
	"strings"
)

type Bencode_Type uint8

const (
	Bencode_String Bencode_Type = iota
	Bencode_Integer
	Bencode_List
	Bencode_Dict
)

type Bencode_DictEntry struct {
	value *Bencode_Value
	key   *Bencode_Value
}

type Bencode_Value struct {
	value_type    Bencode_Type
	integer_value int64
	list_value    []Bencode_Value
	string_value  []byte
	dict_value    []Bencode_DictEntry
}

type Parser_Context struct {
	input []byte
	size  uint64
	pos   uint64
}

func StringifyBencode(v *Bencode_Value, indent ...uint8) (string, error) {

	var tab uint8
	if indent == nil {
		tab = 0
	} else {
		tab = indent[0]
	}

	var ret string
	switch v.value_type {
	case Bencode_Integer:
		ret += strings.Repeat(" ", int(tab)) + "i" + strconv.Itoa(int(v.integer_value)) + "e\n"
	case Bencode_String:
		ret += strings.Repeat(" ", int(tab)) + strconv.Itoa(len(v.string_value)) + ":" + string(v.string_value) + "\n"
	case Bencode_Dict:
		ret += strings.Repeat(" ", int(tab)) + "d\n"
		tab++
		for _, entry := range v.dict_value {

			str, err := StringifyBencode(entry.key, tab)
			if err != nil {
				return "", errors.New("couldn't print bencode value")
			}
			ret += str

			str, err = StringifyBencode(entry.value, tab)
			if err != nil {
				return "", errors.New("couldn't print bencode value")
			}
			ret += str
		}
		tab--
		ret += strings.Repeat(" ", int(tab)) + "e\n"
	case Bencode_List:
		ret += strings.Repeat(" ", int(tab)) + "l\n\t"
		for _, entry := range v.list_value {
			str, err := StringifyBencode(&entry, tab)
			if err != nil {
				return "", errors.New("couldn't print bencode value")
			}
			ret += strings.Repeat(" ", int(tab)) + str
		}
		ret += strings.Repeat(" ", int(tab)) + "e\n"
	default:
		return "", errors.New("couldn't print bencode value")
	}
	return ret, nil
}
