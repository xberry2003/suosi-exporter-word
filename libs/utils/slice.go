package utils

func SliceContainStr(src []string, value string) bool {
	for _, srcValue := range src {
		if srcValue == value {
			return true
		}
	}
	return false
}

func SliceContainInt(src []int, value int) bool {
	for _, srcValue := range src {
		if srcValue == value {
			return true
		}
	}
	return false
}
