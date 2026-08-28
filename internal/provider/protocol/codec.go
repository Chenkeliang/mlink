package protocol

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	MaxHeaderBytes  = 8 << 10
	MaxPayloadBytes = 4 << 20
)

type Decoder struct {
	reader *bufio.Reader
}

func NewDecoder(reader io.Reader) *Decoder {
	return &Decoder{reader: bufio.NewReader(reader)}
}

func (d *Decoder) ReadFrame() (json.RawMessage, error) {
	header, err := d.readHeader()
	if err != nil {
		return nil, err
	}
	length, err := parseContentLength(header)
	if err != nil {
		return nil, err
	}
	if length > MaxPayloadBytes {
		return nil, errors.New("provider frame payload exceeds 4 MiB limit")
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(d.reader, payload); err != nil {
		return nil, errors.New("provider frame payload is truncated")
	}
	if err := validateJSONObject(payload); err != nil {
		return nil, err
	}
	return json.RawMessage(payload), nil
}

func (d *Decoder) readHeader() ([]byte, error) {
	header := make([]byte, 0, 128)
	for {
		char, err := d.reader.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) && len(header) == 0 {
				return nil, io.EOF
			}
			return nil, errors.New("provider frame header is truncated")
		}
		header = append(header, char)
		if len(header) > MaxHeaderBytes {
			return nil, errors.New("provider frame header exceeds 8 KiB limit")
		}
		if len(header) >= 4 && bytes.Equal(header[len(header)-4:], []byte("\r\n\r\n")) {
			return header[:len(header)-4], nil
		}
	}
}

func parseContentLength(header []byte) (int, error) {
	for _, char := range header {
		if char > 0x7f {
			return 0, errors.New("provider frame header must be ASCII")
		}
	}
	count := 0
	length := 0
	for _, rawLine := range bytes.Split(header, []byte("\r\n")) {
		parts := bytes.SplitN(rawLine, []byte{':'}, 2)
		if len(parts) != 2 || len(bytes.TrimSpace(parts[0])) == 0 {
			return 0, errors.New("provider frame contains a malformed header")
		}
		if !strings.EqualFold(string(bytes.TrimSpace(parts[0])), "Content-Length") {
			continue
		}
		count++
		value := string(bytes.TrimSpace(parts[1]))
		if value == "" {
			return 0, errors.New("provider frame Content-Length is empty")
		}
		for i := range len(value) {
			if value[i] < '0' || value[i] > '9' {
				return 0, errors.New("provider frame Content-Length is not decimal")
			}
		}
		parsed, err := strconv.ParseUint(value, 10, 63)
		if err != nil {
			return 0, errors.New("provider frame Content-Length is invalid")
		}
		if parsed > MaxPayloadBytes {
			return 0, errors.New("provider frame payload exceeds 4 MiB limit")
		}
		length = int(parsed)
	}
	if count != 1 {
		return 0, errors.New("provider frame must contain exactly one Content-Length")
	}
	return length, nil
}

type Encoder struct {
	mu     sync.Mutex
	writer io.Writer
}

func NewEncoder(writer io.Writer) *Encoder {
	return &Encoder{writer: writer}
}

func (e *Encoder) WriteFrame(payload []byte) error {
	if err := validateJSONObject(payload); err != nil {
		return err
	}
	frame := make([]byte, 0, len(payload)+48)
	frame = append(frame, fmt.Sprintf("Content-Length: %d\r\n\r\n", len(payload))...)
	frame = append(frame, payload...)
	e.mu.Lock()
	defer e.mu.Unlock()
	for len(frame) > 0 {
		written, err := e.writer.Write(frame)
		if err != nil {
			return fmt.Errorf("write provider frame: %w", err)
		}
		if written <= 0 || written > len(frame) {
			return io.ErrShortWrite
		}
		frame = frame[written:]
	}
	return nil
}

func validateJSONObject(payload []byte) error {
	if len(payload) > MaxPayloadBytes {
		return errors.New("provider frame payload exceeds 4 MiB limit")
	}
	if !utf8.Valid(payload) {
		return errors.New("provider frame payload is not valid UTF-8")
	}
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' || !json.Valid(trimmed) {
		return errors.New("provider frame payload must be one JSON object")
	}
	return nil
}
