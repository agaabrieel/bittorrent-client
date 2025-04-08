package utils

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
		if eq(item, value) {
			return true
		}
	}
	return false
}
