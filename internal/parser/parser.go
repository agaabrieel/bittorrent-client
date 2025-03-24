package parser

import (
	"errors"
	"os"
	"strconv"
	"unicode"
)

func NewParserContext(path string) (*ParserContext, error) {

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	ctx := ParserContext{
		input: data,
		pos:   0,
		size:  uint64(len(data)),
	}

	return &ctx, nil

}

func ParseBencode(ctx *ParserContext) (*BencodeValue, error) {

	var val *BencodeValue
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

func ParseInteger(ctx *ParserContext) (*BencodeValue, error) {

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

	val := BencodeValue{
		ValueType:    BencodeInteger,
		IntegerValue: int64(digit),
	}

	ctx.pos++

	return &val, nil

}

func ParseString(ctx *ParserContext) (*BencodeValue, error) {

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

	val := BencodeValue{
		ValueType:   BencodeString,
		StringValue: make([]byte, str_size),
	}

	copy(val.StringValue, ctx.input[ctx.pos:ctx.pos+uint64(str_size)])

	ctx.pos += uint64(str_size)

	return &val, nil
}

func ParseList(ctx *ParserContext) (*BencodeValue, error) {

	ctx.pos++

	val_list := make([]BencodeValue, 10)

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

	val := BencodeValue{
		ValueType: BencodeList,
		ListValue: val_list,
	}

	ctx.pos++

	return &val, nil
}

func ParseDict(ctx *ParserContext) (*BencodeValue, error) {

	ctx.pos++

	entries := make([]BencodeDictEntry, 0)

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

			entries = append(entries, BencodeDictEntry{
				Key:   key,
				Value: value,
			})

			continue
		}
		break
	}

	val := BencodeValue{
		ValueType: BencodeDict,
		DictValue: entries,
	}

	ctx.pos++

	return &val, nil
}
