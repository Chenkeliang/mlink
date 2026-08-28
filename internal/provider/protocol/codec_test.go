package protocol

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
)

func TestCodecReadsFragmentedConsecutiveUTF8Frames(t *testing.T) {
	payloads := [][]byte{
		[]byte(`{"jsonrpc":"2.0","id":"1","method":"health","params":{"text":"第一行\n第二行"}}`),
		[]byte(`{"jsonrpc":"2.0","id":"2","result":{"状态":"正常"}}`),
	}
	var stream bytes.Buffer
	encoder := NewEncoder(&stream)
	for _, payload := range payloads {
		if err := encoder.WriteFrame(payload); err != nil {
			t.Fatalf("WriteFrame() error = %v", err)
		}
	}

	decoder := NewDecoder(&chunkReader{data: stream.Bytes(), chunkSize: 3})
	for i, want := range payloads {
		got, err := decoder.ReadFrame()
		if err != nil {
			t.Fatalf("ReadFrame(%d) error = %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("ReadFrame(%d) = %s, want %s", i, got, want)
		}
	}
	if _, err := decoder.ReadFrame(); err != io.EOF {
		t.Fatalf("final ReadFrame() error = %v, want io.EOF", err)
	}
}

func TestDecoderAcceptsCaseInsensitiveSingleContentLength(t *testing.T) {
	payload := `{"jsonrpc":"2.0","id":"1","result":{}}`
	stream := fmt.Sprintf("cOnTeNt-LeNgTh: %d\r\nX-Test: value\r\n\r\n%s", len(payload), payload)
	got, err := NewDecoder(strings.NewReader(stream)).ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame() error = %v", err)
	}
	if string(got) != payload {
		t.Fatalf("ReadFrame() = %s, want %s", got, payload)
	}
}

func TestDecoderRejectsMalformedFrames(t *testing.T) {
	validPayload := `{"jsonrpc":"2.0","id":"1","result":{}}`
	tests := []struct {
		name   string
		stream []byte
	}{
		{name: "missing content length", stream: []byte("X-Test: 1\r\n\r\n" + validPayload)},
		{name: "duplicate mixed case", stream: []byte(fmt.Sprintf("Content-Length: %d\r\ncontent-length: %d\r\n\r\n%s", len(validPayload), len(validPayload), validPayload))},
		{name: "negative length", stream: []byte("Content-Length: -1\r\n\r\n")},
		{name: "non decimal length", stream: []byte("Content-Length: 1.5\r\n\r\n")},
		{name: "truncated payload", stream: []byte("Content-Length: 20\r\n\r\n{}")},
		{name: "stdout garbage", stream: []byte("provider starting\n")},
		{name: "invalid utf8", stream: append([]byte("Content-Length: 4\r\n\r\n"), []byte{'{', '"', 0xff, '}'}...)},
		{name: "json scalar", stream: []byte("Content-Length: 4\r\n\r\nnull")},
		{name: "header too large", stream: []byte("X-Fill: " + strings.Repeat("a", MaxHeaderBytes) + "\r\nContent-Length: 2\r\n\r\n{}")},
		{name: "payload too large", stream: []byte(fmt.Sprintf("Content-Length: %d\r\n\r\n", MaxPayloadBytes+1))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewDecoder(bytes.NewReader(tt.stream)).ReadFrame(); err == nil {
				t.Fatal("ReadFrame() error = nil, want malformed frame error")
			}
		})
	}
}

func TestEncoderRejectsInvalidPayloads(t *testing.T) {
	tests := [][]byte{
		[]byte(`[]`),
		[]byte(`null`),
		[]byte(`{"unterminated":`),
		bytes.Repeat([]byte{'x'}, MaxPayloadBytes+1),
		{'{', '"', 0xff, '}'},
	}
	for _, payload := range tests {
		if err := NewEncoder(io.Discard).WriteFrame(payload); err == nil {
			t.Fatalf("WriteFrame(%q) error = nil, want validation error", payload[:min(len(payload), 32)])
		}
	}
}

func TestEncoderSerializesConcurrentFrames(t *testing.T) {
	var output bytes.Buffer
	encoder := NewEncoder(&slowWriter{writer: &output})
	const count = 40
	var wait sync.WaitGroup
	for i := range count {
		wait.Add(1)
		go func(id int) {
			defer wait.Done()
			payload := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":"%d","result":{"id":%d}}`, id, id))
			if err := encoder.WriteFrame(payload); err != nil {
				t.Errorf("WriteFrame(%d) error = %v", id, err)
			}
		}(i)
	}
	wait.Wait()

	decoder := NewDecoder(bytes.NewReader(output.Bytes()))
	seen := make(map[string]struct{}, count)
	for range count {
		frame, err := decoder.ReadFrame()
		if err != nil {
			t.Fatalf("ReadFrame() error = %v", err)
		}
		message, err := ParseMessage(frame)
		if err != nil {
			t.Fatalf("ParseMessage() error = %v", err)
		}
		seen[message.ID] = struct{}{}
	}
	if len(seen) != count {
		t.Fatalf("unique frame IDs = %d, want %d", len(seen), count)
	}
}

type chunkReader struct {
	data      []byte
	chunkSize int
}

func (r *chunkReader) Read(target []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	count := min(len(target), r.chunkSize, len(r.data))
	copy(target, r.data[:count])
	r.data = r.data[count:]
	return count, nil
}

type slowWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

func (w *slowWriter) Write(value []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	total := 0
	for _, char := range value {
		count, err := w.writer.Write([]byte{char})
		total += count
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
