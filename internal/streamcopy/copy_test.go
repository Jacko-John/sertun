// Copy tests cover stream integrity and progress on errors. Relay integration
// tests own cancellation and half-close, so those lifecycles are not duplicated.

package streamcopy

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"testing"
)

// TestCopyProgress covers data returned with EOF or another read error, plus
// partial writes. Method-free endpoints exercise the pooled user-space path.
func TestCopyProgress(t *testing.T) {
	readErr, writeErr := errors.New("read failed"), errors.New("write failed")
	for _, test := range []struct {
		name       string
		size       int
		readErr    error
		writeLimit int
		writeErr   error
		wantN      int
		wantErr    error
	}{
		{"empty", 0, io.EOF, -1, nil, 0, nil},
		{"data_with_eof", (1 << 20) + 17, io.EOF, -1, nil, (1 << 20) + 17, nil},
		{"data_with_error", (1 << 20) + 17, readErr, -1, nil, (1 << 20) + 17, readErr},
		{"short_write", 64, io.EOF, 13, nil, 13, io.ErrShortWrite},
		{"write_error", 64, io.EOF, 13, writeErr, 13, writeErr},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := bytes.Repeat([]byte("x"), test.size)
			source := &finalErrorReader{reader: bytes.NewReader(payload), err: test.readErr}
			destination := &limitedWriter{limit: test.writeLimit, err: test.writeErr}
			n, err := Copy(destination, source)
			if n != int64(test.wantN) || !errors.Is(err, test.wantErr) {
				t.Fatalf("copy: %d bytes, error %v; want %d, %v", n, err, test.wantN, test.wantErr)
			}
			if !bytes.Equal(destination.data.Bytes(), payload[:test.wantN]) {
				t.Fatal("written payload differs")
			}
		})
	}
}

// TestBufferedTCP preserves a partially consumed read-ahead prefix, ignores the
// handshake reader's limit, and leaves the destination's read buffer untouched.
// io.Copy also checks that promoted TCP methods cannot skip buffered payloads.
func TestBufferedTCP(t *testing.T) {
	for _, test := range []struct {
		name string
		copy func(io.Writer, io.Reader) (int64, error)
	}{
		{"sertun", Copy},
		{"stdlib", io.Copy},
	} {
		t.Run(test.name, func(t *testing.T) {
			producer, input := tcpPair(t)
			output, consumer := tcpPair(t)
			payload := bytes.Repeat([]byte("payload\x00"), (1<<17)+3)
			produced := make(chan error, 1)
			go func() {
				_, err := producer.Write(payload)
				producer.CloseWrite()
				produced <- err
			}()
			reader := bufio.NewReader(io.LimitReader(input, 32<<10))
			if _, err := reader.Peek(8); err != nil {
				t.Fatal(err)
			}
			source := WithReadBuffer(input, reader)
			var consumed [3]byte
			if _, err := io.ReadFull(source, consumed[:]); err != nil || !bytes.Equal(consumed[:], payload[:3]) {
				t.Fatalf("initial read: %q, %v", consumed, err)
			}

			// A simultaneous write must not drain the other direction's prefix.
			reply := []byte("pending reply")
			if _, err := consumer.Write(reply); err != nil {
				t.Fatal(err)
			}
			reverse := bufio.NewReader(output)
			if _, err := reverse.Peek(len(reply)); err != nil {
				t.Fatal(err)
			}
			destination := WithReadBuffer(output, reverse)
			type result struct {
				data []byte
				err  error
			}
			drained := make(chan result, 1)
			go func() {
				data, err := io.ReadAll(consumer)
				drained <- result{data, err}
			}()
			n, err := test.copy(destination, source)
			output.CloseWrite()
			if err != nil || n != int64(len(payload)-len(consumed)) {
				t.Fatalf("copy: %d bytes, error %v", n, err)
			}
			gotForward := <-drained
			if gotForward.err != nil || !bytes.Equal(gotForward.data, payload[len(consumed):]) {
				t.Fatalf("forwarded %d bytes, want %d, error %v", len(gotForward.data), len(payload)-len(consumed), gotForward.err)
			}
			if err := <-produced; err != nil {
				t.Fatal(err)
			}
			got := make([]byte, len(reply))
			if _, err := io.ReadFull(destination, got); err != nil || !bytes.Equal(got, reply) {
				t.Fatalf("reverse buffer: %q, %v", got, err)
			}
		})
	}
}

// TestBufferedCopyFailure keeps the unwritten prefix readable and reports only
// delivered bytes, including a writer error returned alongside partial progress.
func TestBufferedCopyFailure(t *testing.T) {
	writeErr := errors.New("write failed")
	for _, failure := range []error{nil, writeErr} {
		producer, input := tcpPair(t)
		payload := []byte("buffered tunnel payload")
		if _, err := producer.Write(payload); err != nil {
			t.Fatal(err)
		}
		producer.CloseWrite()
		reader := bufio.NewReader(input)
		if _, err := reader.Peek(len(payload)); err != nil {
			t.Fatal(err)
		}
		conn := WithReadBuffer(input, reader)
		destination := &limitedWriter{limit: 5, err: failure}
		n, err := Copy(destination, conn)
		wantErr := failure
		if wantErr == nil {
			wantErr = io.ErrShortWrite
		}
		if n != 5 || !errors.Is(err, wantErr) || !bytes.Equal(destination.data.Bytes(), payload[:5]) {
			t.Fatalf("partial prefix: %d bytes, error %v, data %q", n, err, destination.data.Bytes())
		}
		remaining, err := io.ReadAll(conn)
		if err != nil || !bytes.Equal(remaining, payload[5:]) {
			t.Fatalf("remaining prefix: %q, %v", remaining, err)
		}
	}
}

type finalErrorReader struct {
	reader *bytes.Reader
	err    error
}

// Read returns the terminal error alongside the final bytes to catch lost data.
func (r *finalErrorReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if r.reader.Len() == 0 {
		err = r.err
	}
	return n, err
}

type limitedWriter struct {
	data  bytes.Buffer
	limit int
	err   error
}

// Write accepts at most limit bytes per call, optionally with an injected error.
func (w *limitedWriter) Write(p []byte) (int, error) {
	if w.limit >= 0 && len(p) > w.limit {
		p = p[:w.limit]
	}
	n, _ := w.data.Write(p)
	return n, w.err
}

// TestCopyRejectsInvalidWriteCount covers writers that violate the io.Writer
// count contract. Nothing may be counted as delivered.
func TestCopyRejectsInvalidWriteCount(t *testing.T) {
	for _, count := range []int{-1, 2} {
		// Hide bytes.Reader's WriteTo so Copy reaches the buffered loop.
		source := plainReader{bytes.NewReader([]byte("x"))}
		n, err := Copy(&badCountWriter{count: count}, source)
		if n != 0 || !errors.Is(err, errInvalidWrite) {
			t.Fatalf("count %d: got %d, %v; want 0, %v", count, n, err, errInvalidWrite)
		}
	}
}

// TestBufferedWriteRejectsInvalidCount keeps the whole prefix readable when a
// writer reports an impossible count.
func TestBufferedWriteRejectsInvalidCount(t *testing.T) {
	producer, input := tcpPair(t)
	payload := []byte("buffered tunnel payload")
	if _, err := producer.Write(payload); err != nil {
		t.Fatal(err)
	}
	producer.CloseWrite()
	reader := bufio.NewReader(input)
	if _, err := reader.Peek(len(payload)); err != nil {
		t.Fatal(err)
	}
	conn := WithReadBuffer(input, reader)
	n, err := Copy(&badCountWriter{count: len(payload) + 1}, conn)
	if n != 0 || !errors.Is(err, errInvalidWrite) {
		t.Fatalf("buffered write: %d bytes, %v; want 0, %v", n, err, errInvalidWrite)
	}
	remaining, err := io.ReadAll(conn)
	if err != nil || !bytes.Equal(remaining, payload) {
		t.Fatalf("prefix after invalid write: %q, %v", remaining, err)
	}
}

type badCountWriter struct{ count int }

// Write returns an invalid byte count without touching the payload.
func (w *badCountWriter) Write([]byte) (int, error) { return w.count, nil }

// plainReader exposes only Read, selecting the package's buffered loop.
type plainReader struct{ io.Reader }
