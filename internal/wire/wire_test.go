package wire

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

func TestFrameLeavesPayloadUntouched(t *testing.T) {
	var stream bytes.Buffer
	if err := Write(&stream, Message{Type: "ready"}); err != nil {
		t.Fatal(err)
	}
	stream.WriteString("raw payload")
	if _, err := Read(&stream); err != nil {
		t.Fatal(err)
	}
	remaining, _ := io.ReadAll(&stream)
	if string(remaining) != "raw payload" {
		t.Fatalf("frame consumed payload: %q", remaining)
	}
}

func TestRejectInvalidFrame(t *testing.T) {
	oversized := make([]byte, 4)
	binary.BigEndian.PutUint32(oversized, MaxMessageSize+1)
	wrongVersion := []byte(`{"v":99,"type":"login"}`)
	frame := make([]byte, 4+len(wrongVersion))
	binary.BigEndian.PutUint32(frame, uint32(len(wrongVersion)))
	copy(frame[4:], wrongVersion)
	for _, input := range [][]byte{oversized, frame, {0, 0, 0, 10, '{'}} {
		if _, err := Read(bytes.NewReader(input)); err == nil {
			t.Fatal("invalid frame accepted")
		}
	}
}
