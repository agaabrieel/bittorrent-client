package bitfield

type BitfieldMask []byte

func (bf BitfieldMask) HasPiece(idx uint32) bool {
	// suppose len(bf) == 14
	byteIdx := (len(bf) / 8) // = 1, since 1*8 = 8 and 2*8 = 16
	bitIdx := (len(bf) % 8)  // 6, meaning bit at index idx is bf[byteIdx] >> 6
	return bf[byteIdx]>>bitIdx == 0b1
}

func (bf *BitfieldMask) SetPiece(idx uint32) {
	byteIdx := (len(*bf) / 8)
	bitIdx := (len(*bf) % 8)
	(*bf)[byteIdx] = (*bf)[byteIdx] | (1 << bitIdx)
}

func (bf *BitfieldMask) ZeroBitfield() {
	for i := range *bf {
		(*bf)[i] = byte(0x00)
	}
}

func (bf *BitfieldMask) ZeroPiece(idx uint32) {
	(*bf)[idx] = byte(0x00)
}
