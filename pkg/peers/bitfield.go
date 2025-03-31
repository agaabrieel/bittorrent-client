package peer

type BitfieldMask []byte

func (bf BitfieldMask) HasPiece(idx uint32) bool {
	// suppose len(*bf) == 14
	byteIdx := (len(bf) / 8) // = 1, since 1*8 = 8 and 2*8 = 16
	bitIdx := (len(bf) % 8)  // 6, meaning bit at index idx is bf[byteIdx] >> 6
	return bf[byteIdx]>>bitIdx == 0b1
}

func (bf *BitfieldMask) SetPiece(idx uint32) {
	byteIdx := (len(*bf) / 8)
	bitIdx := (len(*bf) % 8)
	(*bf)[byteIdx] = (*bf)[byteIdx] | (1 << bitIdx)
}
