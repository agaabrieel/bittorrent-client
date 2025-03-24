package parser

import (
	"errors"
	"os"
	"strconv"
	"unicode"
)

func NewParserContext(path string) (*Parser_Context, error) {

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	ctx := Parser_Context{
		input: data,
		pos:   0,
		size:  uint64(len(data)),
	}

	return &ctx, nil

}

func ParseBencode(ctx *Parser_Context) (*Bencode_Value, error) {

	var val *Bencode_Value
	var err error

	if ctx.pos >= ctx.size {
		return val, errors.New("PARSER::ERROR::EOF")
	}

	char := rune(ctx.input[ctx.pos])

	switch char {
	case 'd':
		val, err = ParseDict(ctx)
	case 'l':
		val, err = ParseList(ctx)
	case 'i':
		val, err = ParseInteger(ctx)
	default:
		if unicode.IsDigit(rune(char)) {
			val, err = ParseString(ctx)
		} else {
			return nil, errors.New("PARSER::ERROR::UNKNOWN_CHAR")
		}
	}

	return val, err

}

func ParseInteger(ctx *Parser_Context) (*Bencode_Value, error) {

	ctx.pos++ // Get to the next token

	start := ctx.pos

	for {
		if rune(ctx.input[ctx.pos]) != 'e' {
			ctx.pos++
			continue
		}
		break
	}

	digit, err := strconv.Atoi(string(ctx.input[start:ctx.pos]))
	if err != nil {
		return nil, err
	}

	val := Bencode_Value{
		value_type:    Bencode_Integer,
		integer_value: int64(digit),
	}

	ctx.pos++

	return &val, nil

}

func ParseString(ctx *Parser_Context) (*Bencode_Value, error) {

	start := ctx.pos

	for {
		if rune(ctx.input[ctx.pos]) != ':' {
			ctx.pos++
			continue
		}
		break
	}

	str_size, err := strconv.Atoi(string(ctx.input[start:ctx.pos]))
	if err != nil {
		return nil, err
	}
	ctx.pos++

	val := Bencode_Value{
		value_type:   Bencode_String,
		string_value: make([]byte, str_size),
	}

	copy(val.string_value, ctx.input[ctx.pos:ctx.pos+uint64(str_size)])

	ctx.pos += uint64(str_size)

	return &val, nil
}

func ParseList(ctx *Parser_Context) (*Bencode_Value, error) {

	ctx.pos++

	val_list := make([]Bencode_Value, 10)

	for {
		if ctx.input[ctx.pos] != 'e' {

			if ctx.pos >= uint64(len(ctx.input)) {
				return nil, errors.New("ERROR::PARSER::NO_MATCHING_END_TAG")
			}

			value, err := ParseBencode(ctx)
			if err != nil {
				return nil, err
			}

			val_list = append(val_list, *value)

			continue
		}
		break
	}

	val := Bencode_Value{
		value_type: Bencode_List,
		list_value: val_list,
	}

	ctx.pos++

	return &val, nil
}

func ParseDict(ctx *Parser_Context) (*Bencode_Value, error) {

	ctx.pos++

	var entries []Bencode_DictEntry

	for {
		if ctx.input[ctx.pos] != 'e' {

			if ctx.pos >= uint64(len(ctx.input)) {
				return nil, errors.New("ERROR::PARSER::NO_MATCHING_END_TAG")
			}

			key, err := ParseBencode(ctx)
			if err != nil {
				return nil, err
			}

			value, err := ParseBencode(ctx)
			if err != nil {
				return nil, err
			}

			dict_entry := Bencode_DictEntry{
				key:   key,
				value: value,
			}

			entries = append(entries, dict_entry)

			continue
		}
		break
	}

	val := Bencode_Value{
		value_type: Bencode_Dict,
		dict_value: entries,
	}

	ctx.pos++

	return &val, nil
}
