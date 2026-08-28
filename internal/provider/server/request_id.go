package server

const requestIDReorderWindow = maximumConcurrency

type requestIDTracker struct {
	highest string
	seen    map[string]struct{}
}

func (t *requestIDTracker) Accept(id string) bool {
	normalized := normalizeDecimalID(id)
	if normalized == "" {
		return false
	}
	if _, duplicate := t.seen[normalized]; duplicate {
		return false
	}
	if t.highest != "" && compareDecimalID(normalized, t.highest) < 0 &&
		compareDecimalID(addSmallDecimal(normalized, requestIDReorderWindow), t.highest) < 0 {
		return false
	}
	if t.highest == "" || compareDecimalID(normalized, t.highest) > 0 {
		t.highest = normalized
	}
	if t.seen == nil {
		t.seen = make(map[string]struct{}, requestIDReorderWindow+1)
	}
	t.seen[normalized] = struct{}{}
	t.prune()
	return true
}

func (t *requestIDTracker) prune() {
	for id := range t.seen {
		if compareDecimalID(addSmallDecimal(id, requestIDReorderWindow), t.highest) < 0 {
			delete(t.seen, id)
		}
	}
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

func addSmallDecimal(id string, increment int) string {
	result := []byte(id)
	carry := increment
	for index := len(result) - 1; index >= 0 && carry > 0; index-- {
		value := int(result[index]-'0') + carry
		result[index] = byte(value%10) + '0'
		carry = value / 10
	}
	for carry > 0 {
		result = append([]byte{byte(carry%10) + '0'}, result...)
		carry /= 10
	}
	return string(result)
}
