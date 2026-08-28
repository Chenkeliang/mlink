package server

type requestIDTracker struct {
	highest string
}

func (t *requestIDTracker) Accept(id string) bool {
	normalized := normalizeDecimalID(id)
	if normalized == "" {
		return false
	}
	if t.highest != "" && compareDecimalID(normalized, t.highest) <= 0 {
		return false
	}
	t.highest = normalized
	return true
}

func normalizeDecimalID(id string) string {
	if id == "" {
		return ""
	}
	for index := range len(id) {
		if id[index] < '0' || id[index] > '9' {
			return ""
		}
	}
	index := 0
	for index < len(id)-1 && id[index] == '0' {
		index++
	}
	return id[index:]
}

func compareDecimalID(left, right string) int {
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}
