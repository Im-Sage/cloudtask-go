func isAnagram(s string, t string) bool {
	if len(s) != len(t) {
		return false
	}

	var count [26]int // 切片
	for _, ch := range s {
		count[ch-'a']++
	}
	for _, ch := range t {
		count[ch-'a']--
	}
	for _, n := range count {
		if n != 0 {
			return false
		}
	}
	return true

}