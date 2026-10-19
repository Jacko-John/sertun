// Package wire encodes the control frames shared by client and server. A frame
// is a four-byte big-endian length followed by a bounded JSON message; the
// payload itself travels outside this encoding.
package wire

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

const (
	version        = 1
	maxMessageSize = 16 * 1024
)

// Message is one control frame. Only the fields relevant to Type are set.
type Message struct {
	Version  int      `json:"v"`
	Type     string   `json:"type"`
	Client   string   `json:"client,omitempty"`
	Token    string   `json:"token,omitempty"`
	Session  string   `json:"session,omitempty"`
	Services []string `json:"services,omitempty"`
	Service  string   `json:"service,omitempty"`
	Request  string   `json:"request,omitempty"`
	Code     string   `json:"code,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// Read consumes exactly one frame, leaving subsequent payload bytes untouched.
func Read(r io.Reader) (Message, error) {
	var message Message
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return message, err
	}
	n := binary.BigEndian.Uint32(header[:])
	if n == 0 || n > maxMessageSize {
		return message, fmt.Errorf("invalid control message size: %d", n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return message, err
	}
	if err := json.Unmarshal(body, &message); err != nil {
		return message, fmt.Errorf("invalid control message: %w", err)
	}
	if message.Version != version {
		return message, fmt.Errorf("unsupported protocol version: %d", message.Version)
	}
	return message, nil
}

// Write serializes one frame and handles short writes.
func Write(w io.Writer, message Message) error {
	message.Version = version
	body, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if len(body) > maxMessageSize {
		return fmt.Errorf("control message exceeds %d bytes", maxMessageSize)
	}
	frame := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(body)))
	copy(frame[4:], body)
	for len(frame) > 0 {
		n, err := w.Write(frame)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		frame = frame[n:]
	}
	return nil
}

// Sender serializes concurrent control writes and bounds a stalled peer's write.
// Timeout must be positive; Send refreshes the write deadline on every call.
type Sender struct {
	Conn    net.Conn      // Control connection owned by the caller.
	Timeout time.Duration // Per-write deadline; must be positive.
	mu      sync.Mutex
}

// Send writes one frame under the configured deadline.
func (s *Sender) Send(message Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.Conn.SetWriteDeadline(time.Now().Add(s.Timeout)); err != nil {
		return err
	}
	return Write(s.Conn, message)
}

// NewID returns a 128-bit random identifier as lowercase hex.
func NewID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}
