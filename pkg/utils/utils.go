package utils

import "math"

func Contains[T comparable](arr []T, value T) bool {
	for _, v := range arr {
		if v == value {
			return true
		}
	}
	return false
}

func ContainsFunc[T any](slice []T, value T, eq func(a, b T) bool) bool {
	for _, item := range slice {
		return eq(item, value)
	}
	return false
}

func ByteArrToInt64Arr(bytes []byte) []int64 {
	int64_arr := make([]int64, int(math.Ceil(float64(len(bytes))/8)))
	var int64_idx, byte_idx int
	for idx, b := range bytes {
		byte_idx = idx % 8
		int64_idx = idx / 8
		int64_arr[int64_idx] = int64_arr[int64_idx] ^ (int64(b) << 8 * int64(byte_idx))
	}
	return int64_arr
}

func ByteArrToUint64Arr(bytes []byte) []uint64 {
	uint64_arr := make([]uint64, int(math.Ceil(float64(len(bytes))/8)))
	var int64_idx, byte_idx int
	for idx, b := range bytes {
		byte_idx = idx % 8
		int64_idx = idx / 8
		uint64_arr[int64_idx] = uint64_arr[int64_idx] ^ (uint64(b) << 8 * uint64(byte_idx))
	}
	return uint64_arr
}

// 0b0000000000000000000000000000000000000000000000000000000000000000
// ^
// 0b00000000
