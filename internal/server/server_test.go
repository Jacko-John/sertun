package server

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sertun/internal/client"
	"sertun/internal/config"
	"sertun/internal/relay"
	"sertun/internal/wire"
)

const testToken = "test-only-shared-token"

func TestReverseTCP(t *testing.T) {
	for _, useProxy := range []bool{false, true} {
		name := "direct"
		if useProxy {
			name = "http_connect"
		}
		t.Run(name, func(t *testing.T) {
			echo := startEndpoint(t, func(ctx context.Context, conn net.Conn) {
				_, _ = io.Copy(conn, conn)
			})
			afterEOF := startEndpoint(t, func(ctx context.Context, conn net.Conn) {
				data, err := io.ReadAll(conn)
				if err == nil {
					_, _ = conn.Write(append([]byte("reply:"), data...))
				}
			})
			cfg := serverConfig()
			cfg.Clients[0].Services = append(cfg.Clients[0].Services, config.ServerService{Name: "after-eof", Listen: "127.0.0.1:0"})
			srv := startServer(t, cfg)
			cc := clientConfig(srv, echo)
			cc.Services = append(cc.Services, config.ClientService{Name: "after-eof", Local: afterEOF})
			var requests atomic.Int64
			if useProxy {
				cc.Proxy = "http://" + startEndpoint(t, func(ctx context.Context, conn net.Conn) {
					reader := bufio.NewReader(conn)
					request, err := http.ReadRequest(reader)
					if err != nil || request.Method != http.MethodConnect {
						return
					}
					requests.Add(1)
					target, err := (&net.Dialer{}).DialContext(ctx, "tcp", request.Host)
					if err != nil {
						return
					}
					defer target.Close()
					if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
						return
					}
					_ = relay.Bridge(ctx, &proxyConn{Conn: conn, reader: reader}, target)
				})
			}
			startClient(t, cc)
			waitSession(t, srv, "")
			// Distinct simultaneous payloads expose accidental cross-pairing.
			echoAddress := mapped(t, srv, "echo")
			results := make(chan error, 8)
			for i := range 8 {
				go func() {
					payload := bytes.Repeat([]byte{byte(i + 1)}, 64*1024)
					results <- exchange(echoAddress, payload, payload)
				}()
			}
			for range 8 {
				if err := <-results; err != nil {
					t.Error(err)
				}
			}
			// Cover promotion and EOF just below/at/above the user-space buffer
			// threshold, including a response sent only after request EOF.
			for _, size := range []int{7, (1 << 20) - 1, 1 << 20, (1 << 20) + 1} {
				payload := bytes.Repeat([]byte("r"), size)
				if err := exchange(mapped(t, srv, "after-eof"), payload, append([]byte("reply:"), payload...)); err != nil {
					t.Fatalf("after EOF, %d bytes: %v", size, err)
				}
			}
			if useProxy && requests.Load() < 2 {
				t.Fatal("business connections did not use CONNECT")
			}
		})
	}
}

func TestRegistrationRejected(t *testing.T) {
	srv := startServer(t, serverConfig())
	for _, reason := range []string{"token", "services"} {
		t.Run(reason, func(t *testing.T) {
			cfg := clientConfig(srv, "127.0.0.1:22")
			if reason == "token" {
				cfg.Token = "incorrect-token"
			} else {
				cfg.Services[0].Name = "unconfigured"
			}
			cli, err := client.New(cfg, quietLogger())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if err := cli.Run(ctx); !errors.Is(err, client.ErrRejected) {
				t.Fatalf("want registration rejection, got %v", err)
			}
		})
	}
}

func TestReconnectAndShutdown(t *testing.T) {
	backend := startEndpoint(t, func(ctx context.Context, conn net.Conn) { _, _ = io.Copy(conn, conn) })
	srv := startServer(t, serverConfig())
	runner := startClient(t, clientConfig(srv, backend))
	previous := waitSession(t, srv, "")
	visitor := dialTCP(t, mapped(t, srv, "echo"))
	if _, err := visitor.Write([]byte("before")); err != nil {
		t.Fatal(err)
	}
	var before [6]byte
	if _, err := io.ReadFull(visitor, before[:]); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	control := srv.sessions["home"].sender.Conn
	srv.mu.Unlock()
	control.Close()
	expectClosed(t, visitor)
	waitSession(t, srv, previous)
	if err := exchange(mapped(t, srv, "echo"), []byte("after"), []byte("after")); err != nil {
		t.Fatal(err)
	}
	runner.stop(t)
	visitor = dialTCP(t, mapped(t, srv, "echo"))
	expectClosed(t, visitor)
}

func TestLimitsTimeoutAndOneTimePairing(t *testing.T) {
	cfg := serverConfig()
	cfg.Timeout = config.Duration(300 * time.Millisecond)
	cfg.Clients[0].MaxConnections = 1
	cfg.Clients[0].MaxPending = 1
	srv := startServer(t, cfg)
	control := dialTCP(t, srv.control.Addr().String())
	send(t, control, wire.Message{Type: "login", Client: "home", Token: testToken, Services: []string{"echo"}})
	login := receive(t, control)
	if login.Type != "login_ok" {
		t.Fatal("registration failed")
	}
	waitSession(t, srv, "")
	first := dialTCP(t, mapped(t, srv, "echo"))
	request := receive(t, control)
	if request.Type != "open" {
		t.Fatal("missing work request")
	}
	second := dialTCP(t, mapped(t, srv, "echo"))
	expectClosed(t, second)
	expectClosed(t, first) // No work connection: pairing must time out.
	stale := dialTCP(t, srv.control.Addr().String())
	send(t, stale, wire.Message{Type: "attach", Client: "home", Session: login.Session, Request: request.Request})
	if receive(t, stale).Type != "error" {
		t.Fatal("expired request was accepted")
	}
	third := dialTCP(t, mapped(t, srv, "echo"))
	request = receive(t, control)
	attach := wire.Message{Type: "attach", Client: "home", Session: login.Session, Request: request.Request}
	work := dialTCP(t, srv.control.Addr().String())
	send(t, work, attach)
	if receive(t, work).Type != "ready" {
		t.Fatal("connection limit was not released after timeout")
	}
	duplicate := dialTCP(t, srv.control.Addr().String())
	send(t, duplicate, attach)
	if receive(t, duplicate).Type != "error" {
		t.Fatal("request was used more than once")
	}
	if _, err := work.Write([]byte("paired")); err != nil {
		t.Fatal(err)
	}
	var payload [6]byte
	if _, err := io.ReadFull(third, payload[:]); err != nil || string(payload[:]) != "paired" {
		t.Fatalf("paired payload: %q, %v", payload, err)
	}
	control.Close()
	expectClosed(t, work)
	expectClosed(t, third)
}

func serverConfig() config.Server {
	return config.Server{
		Listen: "127.0.0.1:0", Timeout: config.Duration(3 * time.Second),
		HeartbeatTimeout: config.Duration(5 * time.Second),
		Clients: []config.ServerClient{{
			ID: "home", Token: testToken, MaxConnections: 16, MaxPending: 16,
			Services: []config.ServerService{{Name: "echo", Listen: "127.0.0.1:0"}},
		}},
	}
}

func clientConfig(srv *Server, backend string) config.Client {
	return config.Client{
		Server: srv.control.Addr().String(), ID: "home", Token: testToken,
		Timeout: config.Duration(3 * time.Second), MaxConnections: 16,
		HeartbeatInterval: config.Duration(100 * time.Millisecond), HeartbeatTimeout: config.Duration(5 * time.Second),
		ReconnectMin: config.Duration(10 * time.Millisecond), ReconnectMax: config.Duration(50 * time.Millisecond),
		Services: []config.ClientService{{Name: "echo", Local: backend}},
	}
}

type running struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

func (r *running) stop(t *testing.T) {
	t.Helper()
	r.cancel()
	select {
	case <-r.done:
		if r.err != nil {
			t.Errorf("runner stopped with error: %v", r.err)
		}
	case <-time.After(5 * time.Second):
		t.Error("runner did not release its connections on shutdown")
	}
}

func startServer(t *testing.T, cfg config.Server) *Server {
	t.Helper()
	srv, err := New(cfg, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runner := &running{cancel: cancel, done: make(chan struct{})}
	t.Cleanup(func() { runner.stop(t) })
	go func() { runner.err = srv.Run(ctx); close(runner.done) }()
	select {
	case <-srv.ready:
	case <-runner.done:
		t.Fatalf("server failed to start: %v", runner.err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not start")
	}
	return srv
}

func startClient(t *testing.T, cfg config.Client) *running {
	t.Helper()
	cli, err := client.New(cfg, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runner := &running{cancel: cancel, done: make(chan struct{})}
	t.Cleanup(func() { runner.stop(t) })
	go func() { runner.err = cli.Run(ctx); close(runner.done) }()
	return runner
}

func startEndpoint(t *testing.T, handle func(context.Context, net.Conn)) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var jobs sync.WaitGroup
	jobs.Add(1)
	go func() {
		defer jobs.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			jobs.Add(1)
			go func() {
				defer jobs.Done()
				defer conn.Close()
				stop := context.AfterFunc(ctx, func() { conn.Close() })
				defer stop()
				handle(ctx, conn)
			}()
		}
	}()
	t.Cleanup(func() {
		cancel()
		listener.Close()
		done := make(chan struct{})
		go func() { jobs.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("endpoint did not stop")
		}
	})
	return listener.Addr().String()
}

func waitSession(t *testing.T, srv *Server, previous string) string {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		srv.mu.Lock()
		sess := srv.sessions["home"]
		id := ""
		if sess != nil && sess.ready && sess.id != previous {
			id = sess.id
		}
		srv.mu.Unlock()
		if id != "" {
			return id
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("client session did not become ready")
		}
	}
}

func mapped(t *testing.T, srv *Server, name string) string {
	t.Helper()
	for _, listener := range srv.listeners {
		if listener.service == name {
			return listener.Addr().String()
		}
	}
	t.Fatalf("missing service %q", name)
	return ""
}

func exchange(address string, payload, expected []byte) error {
	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(payload); err != nil {
		return err
	}
	if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
		return err
	}
	got, err := io.ReadAll(conn)
	if err != nil || !bytes.Equal(got, expected) {
		return fmt.Errorf("payload mismatch: got %d bytes, want %d, error %v", len(got), len(expected), err)
	}
	return nil
}

func dialTCP(t *testing.T, address string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	return conn
}

func expectClosed(t *testing.T, conn net.Conn) {
	t.Helper()
	var data [1]byte
	_, err := conn.Read(data[:])
	var networkError net.Error
	if err == nil || (errors.As(err, &networkError) && networkError.Timeout()) {
		t.Fatalf("connection was not released: %v", err)
	}
}

func send(t *testing.T, conn net.Conn, message wire.Message) {
	t.Helper()
	if err := wire.Write(conn, message); err != nil {
		t.Fatal(err)
	}
}

func receive(t *testing.T, conn net.Conn) wire.Message {
	t.Helper()
	message, err := wire.Read(conn)
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type proxyConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *proxyConn) Read(p []byte) (int, error) { return c.reader.Read(p) }
func (c *proxyConn) CloseWrite() error {
	return c.Conn.(interface{ CloseWrite() error }).CloseWrite()
}
