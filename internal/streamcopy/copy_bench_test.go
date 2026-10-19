// TCP copy benchmarks isolate the copy policy with real sockets.
// Their throughput describes one relay stage, not an end-to-end tunnel.

package streamcopy

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

type readOnly struct{ io.Reader }
type writeOnly struct{ io.Writer }

// BenchmarkTCPCopy compares the standard copy path with the production policy.
func BenchmarkTCPCopy(b *testing.B) {
	for _, test := range []struct {
		name     string
		copy     func(io.Writer, io.Reader) (int64, error)
		buffered bool
	}{
		{"stdlib", io.Copy, false},
		{"sertun", Copy, false},
		// Hide the buffered connection's copy methods to model the previous
		// CONNECT wrapper, which exposed only net.Conn's Read and Write.
		{"stdlib_buffered", func(dst io.Writer, src io.Reader) (int64, error) {
			return io.Copy(dst, readOnly{src})
		}, true},
		{"sertun_buffered", Copy, true},
	} {
		b.Run(test.name, func(b *testing.B) {
			producer, input := tcpPair(b)
			output, consumer := tcpPair(b)
			var source io.Reader = input
			var prefix int64
			if test.buffered {
				if _, err := producer.Write([]byte{0}); err != nil {
					b.Fatal(err)
				}
				reader := bufio.NewReader(io.LimitReader(input, 32<<10))
				if _, err := reader.Peek(1); err != nil {
					b.Fatal(err)
				}
				source = WithReadBuffer(input, reader)
				prefix = 1
			}
			type result struct {
				n   int64
				err error
			}
			copied, drained := make(chan result, 1), make(chan result, 1)
			go func() {
				n, err := test.copy(output, source)
				if err != nil {
					input.Close()
				}
				output.CloseWrite()
				copied <- result{n, err}
			}()
			go func() {
				// Keep the drain buffer large so it does not impose the policy
				// being compared on the relay stage itself.
				n, err := io.CopyBuffer(writeOnly{io.Discard}, readOnly{consumer}, make([]byte, 512<<10))
				if err != nil {
					consumer.Close()
				}
				drained <- result{n, err}
			}()
			payload := make([]byte, 64<<10)
			b.SetBytes(int64(len(payload)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := producer.Write(payload); err != nil {
					b.Fatal(err)
				}
			}
			producer.CloseWrite()
			for _, result := range []result{<-copied, <-drained} {
				if result.err != nil || result.n != int64(b.N)*int64(len(payload))+prefix {
					b.Fatalf("transfer: %+v", result)
				}
			}
			b.StopTimer()
		})
	}
}

// BenchmarkCopyBuffers measures warm per-stream allocation, excluding fixtures.
// Cold pool entries still allocate their working buffers. This in-memory check
// does not describe network throughput or process RSS.
func BenchmarkCopyBuffers(b *testing.B) {
	for _, size := range []int{64, 4 << 20} {
		for _, test := range []struct {
			name string
			copy func(io.Writer, io.Reader) (int64, error)
		}{
			{"stdlib", io.Copy},
			{"sertun", Copy},
		} {
			b.Run(fmt.Sprintf("%s/%dB", test.name, size), func(b *testing.B) {
				payload := make([]byte, size)
				reader := bytes.NewReader(payload)
				var source io.Reader = readOnly{reader}
				var destination io.Writer = writeOnly{io.Discard}
				if _, err := test.copy(destination, source); err != nil {
					b.Fatal(err)
				}
				b.SetBytes(int64(size))
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					reader.Reset(payload)
					n, err := test.copy(destination, source)
					if err != nil || n != int64(size) {
						b.Fatalf("copy: %d bytes, error %v", n, err)
					}
				}
			})
		}
	}
}

// tcpPair returns both ends of a bounded, real loopback TCP connection.
func tcpPair(b testing.TB) (*net.TCPConn, *net.TCPConn) {
	b.Helper()
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		b.Fatal(err)
	}
	defer listener.Close()
	_ = listener.SetDeadline(time.Now().Add(5 * time.Second))
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(b.Context(), "tcp", listener.Addr().String())
	if err != nil {
		b.Fatal(err)
	}
	client := conn.(*net.TCPConn)
	server, err := listener.AcceptTCP()
	if err != nil {
		client.Close()
		b.Fatal(err)
	}
	client.SetDeadline(time.Now().Add(time.Minute))
	server.SetDeadline(time.Now().Add(time.Minute))
	b.Cleanup(func() { client.Close(); server.Close() })
	return client, server
}
