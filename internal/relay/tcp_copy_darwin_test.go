// TCP copy benchmarks isolate the macOS buffer policy with real sockets.
// Their throughput describes one relay stage, not an end-to-end tunnel.
package relay

import (
	"io"
	"net"
	"testing"
	"time"
)

// BenchmarkTCPCopy compares the standard copy path with the production policy.
func BenchmarkTCPCopy(b *testing.B) {
	for _, test := range []struct {
		name string
		copy func(net.Conn, net.Conn) (int64, error)
	}{
		{"stdlib", func(dst, src net.Conn) (int64, error) { return io.Copy(dst, src) }},
		{"adaptive", copyPayload},
	} {
		b.Run(test.name, func(b *testing.B) {
			producer, input := benchmarkTCPPair(b)
			defer producer.Close()
			defer input.Close()
			output, consumer := benchmarkTCPPair(b)
			defer output.Close()
			defer consumer.Close()
			type result struct {
				n   int64
				err error
			}
			copied, drained := make(chan result, 1), make(chan result, 1)
			go func() {
				n, err := test.copy(output, input)
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
			b.ResetTimer()
			for range b.N {
				if _, err := producer.Write(payload); err != nil {
					b.Fatal(err)
				}
			}
			producer.CloseWrite()
			for _, result := range []result{<-copied, <-drained} {
				if result.err != nil || result.n != int64(b.N)*int64(len(payload)) {
					b.Fatalf("transfer: %+v", result)
				}
			}
			b.StopTimer()
		})
	}
}

// benchmarkTCPPair returns both ends of a bounded, real loopback TCP connection.
func benchmarkTCPPair(b *testing.B) (*net.TCPConn, *net.TCPConn) {
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
	return client, server
}
