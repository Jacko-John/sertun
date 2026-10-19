// Tests cover CONNECT authentication, cancellation, and read-ahead handling.

package dial

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestCONNECTPreservesPayloadAndAuthenticates(t *testing.T) {
	// A payload larger than the response-header limit also checks that the
	// limited handshake reader is not accidentally retained for tunnel reads.
	payload := bytes.Repeat([]byte("server-first payload\n"), 4096)
	proxy, done := proxyOnce(t, func(conn net.Conn, request *http.Request) error {
		if request.Method != http.MethodConnect || request.Host != "target.example:7000" {
			return fmt.Errorf("unexpected CONNECT target")
		}
		if request.Header.Get("Proxy-Authorization") != "Basic dXNlcjpwYXNz" {
			return fmt.Errorf("missing proxy authentication")
		}
		_, err := conn.Write(append([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"), payload...))
		return err
	})
	dialer, err := New("http://user:pass@"+proxy, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := dialer.DialContext(t.Context(), "target.example:7000")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := io.ReadAll(conn)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("tunnel payload: got %d bytes, want %d, error %v", len(got), len(payload), err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestCONNECTCancellation(t *testing.T) {
	requestSeen := make(chan struct{})
	proxy, done := proxyOnce(t, func(conn net.Conn, _ *http.Request) error {
		close(requestSeen)
		_, err := io.Copy(io.Discard, conn)
		return err
	})
	dialer, err := New("http://"+proxy, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		conn, err := dialer.DialContext(ctx, "target.example:7000")
		if conn != nil {
			conn.Close()
		}
		result <- err
	}()
	select {
	case <-requestSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("proxy did not receive CONNECT")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want cancellation, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled CONNECT did not return")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("proxy connection was not closed")
	}
}

func TestCONNECTRejectsFailedOrOversizedResponse(t *testing.T) {
	for _, response := range []string{
		"HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n\r\n",
		"HTTP/1.1 200 OK\r\nX-Large: " + string(bytes.Repeat([]byte("a"), 33*1024)) + "\r\n\r\n",
	} {
		proxy, done := proxyOnce(t, func(conn net.Conn, _ *http.Request) error {
			_, _ = io.WriteString(conn, response)
			return nil
		})
		dialer, err := New("http://"+proxy, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		conn, err := dialer.DialContext(t.Context(), "target.example:7000")
		if conn != nil {
			conn.Close()
		}
		if err == nil {
			t.Fatal("invalid CONNECT response was accepted")
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func proxyOnce(t *testing.T, handle func(net.Conn, *http.Request) error) (string, <-chan error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		request, err := http.ReadRequest(bufio.NewReader(conn))
		if err == nil {
			err = handle(conn, request)
		}
		done <- err
	}()
	return listener.Addr().String(), done
}
