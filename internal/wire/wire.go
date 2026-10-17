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
	Version        = 1
	MaxMessageSize = 16 * 1024
)

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
	if n == 0 || n > MaxMessageSize {
		return message, fmt.Errorf("invalid control message size: %d", n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return message, err
	}
	if err := json.Unmarshal(body, &message); err != nil {
		return message, fmt.Errorf("invalid control message")
	}
	if message.Version != Version {
		return message, fmt.Errorf("unsupported protocol version: %d", message.Version)
	}
	return message, nil
}

func Write(w io.Writer, message Message) error {
	message.Version = Version
	body, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if len(body) > MaxMessageSize {
		return fmt.Errorf("control message exceeds %d bytes", MaxMessageSize)
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
type Sender struct {
	Conn    net.Conn
	Timeout time.Duration
	mu      sync.Mutex
}

func (s *Sender) Send(message Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.Conn.SetWriteDeadline(time.Now().Add(s.Timeout)); err != nil {
		return err
	}
	return Write(s.Conn, message)
}

func NewID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}
