package host

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"sync"
)

var redactedMarker = []byte("[REDACTED]")

type redactingBuffer struct {
	mu           sync.Mutex
	secrets      [][]byte
	maxSecretLen int
	pending      []byte
	output       tailBuffer
}

func newRedactingBuffer(secrets []string, capacity int) *redactingBuffer {
	filtered := make([][]byte, 0, len(secrets))
	maxLength := 0
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		value := []byte(secret)
		filtered = append(filtered, append([]byte(nil), value...))
		if len(value) > maxLength {
			maxLength = len(value)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return len(filtered[i]) > len(filtered[j]) })
	return &redactingBuffer{secrets: filtered, maxSecretLen: maxLength, output: tailBuffer{capacity: capacity}}
}

func (b *redactingBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.maxSecretLen == 0 {
		b.output.write(value)
		return len(value), nil
	}
	b.pending = append(b.pending, value...)
	for len(b.pending) >= b.maxSecretLen {
		if matched := b.matchPrefix(); matched > 0 {
			b.output.write(redactedMarker)
			b.pending = b.pending[matched:]
			continue
		}
		b.output.write(b.pending[:1])
		b.pending = b.pending[1:]
	}
	return len(value), nil
}

func (b *redactingBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	pending := redactAll(b.pending, b.secrets)
	combined := append(b.output.bytes(), pending...)
	if len(combined) > b.output.capacity {
		combined = combined[len(combined)-b.output.capacity:]
	}
	return string(combined)
}

func (b *redactingBuffer) matchPrefix() int {
	for _, secret := range b.secrets {
		if bytes.HasPrefix(b.pending, secret) {
			return len(secret)
		}
	}
	return 0
}

type tailBuffer struct {
	capacity int
	data     []byte
}

func (b *tailBuffer) write(value []byte) {
	if b.capacity <= 0 || len(value) == 0 {
		return
	}
	if len(value) >= b.capacity {
		b.data = append(b.data[:0], value[len(value)-b.capacity:]...)
		return
	}
	overflow := len(b.data) + len(value) - b.capacity
	if overflow > 0 {
		copy(b.data, b.data[overflow:])
		b.data = b.data[:len(b.data)-overflow]
	}
	b.data = append(b.data, value...)
}

func (b *tailBuffer) bytes() []byte {
	return append([]byte(nil), b.data...)
}

func redactAll(value []byte, secrets [][]byte) []byte {
	result := append([]byte(nil), value...)
	for _, secret := range secrets {
		result = bytes.ReplaceAll(result, secret, redactedMarker)
	}
	return result
}

func jsonContainsSecret(raw []byte, secrets []string) (bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return false, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return false, errors.New("multiple JSON values")
		}
		return false, err
	}
	filtered := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		if secret != "" {
			filtered = append(filtered, secret)
		}
	}
	return valueContainsSecret(value, filtered), nil
}

func valueContainsSecret(value any, secrets []string) bool {
	switch typed := value.(type) {
	case string:
		for _, secret := range secrets {
			if bytes.Contains([]byte(typed), []byte(secret)) {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if valueContainsSecret(item, secrets) {
				return true
			}
		}
	case map[string]any:
		for _, item := range typed {
			if valueContainsSecret(item, secrets) {
				return true
			}
		}
	}
	return false
}
