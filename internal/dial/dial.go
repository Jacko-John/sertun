// Package dial opens direct TCP connections or tunnels through an HTTP
// CONNECT proxy. A configured proxy is never bypassed on failure.
package dial

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"sertun/internal/config"
	"sertun/internal/streamcopy"
)

// Dialer opens connections with one proxy setting and one timeout.
type Dialer struct {
	proxy   *url.URL
	timeout time.Duration
}

// New parses the proxy URL. An empty string selects direct connections.
func New(proxy string, timeout time.Duration) (*Dialer, error) {
	u, err := config.ParseProxy(proxy)
	if err != nil {
		return nil, err
	}
	return &Dialer{proxy: u, timeout: timeout}, nil
}

// DialContext connects to address, performing a CONNECT handshake when a proxy
// is configured. Bytes read ahead during the handshake remain available to the
// tunnel through the returned connection.
func (d *Dialer) DialContext(ctx context.Context, address string) (result net.Conn, err error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	network := &net.Dialer{KeepAlive: 30 * time.Second}
	if d.proxy == nil {
		return network.DialContext(ctx, "tcp", address)
	}

	port := d.proxy.Port()
	if port == "" {
		port = "80"
	}
	conn, err := network.DialContext(ctx, "tcp", net.JoinHostPort(d.proxy.Hostname(), port))
	if err != nil {
		return nil, fmt.Errorf("connect to HTTP proxy: %w", err)
	}
	// Whichever way this returns, a connection that was not handed to the
	// caller must be closed and a canceled context must win over I/O errors.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	ok := false
	defer func() {
		stop()
		if !ok {
			conn.Close()
			if ctx.Err() != nil {
				err = ctx.Err()
			}
		}
	}()

	// DialContext always installs a timeout, so the deadline is present.
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	request := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Host: address},
		Host:   address,
		Header: make(http.Header),
	}
	if user := d.proxy.User; user != nil {
		// The bool only reports whether a password was present; empty is valid.
		password, _ := user.Password()
		credentials := base64.StdEncoding.EncodeToString([]byte(user.Username() + ":" + password))
		request.Header.Set("Proxy-Authorization", "Basic "+credentials)
	}
	if err := request.Write(conn); err != nil {
		return nil, fmt.Errorf("write HTTP CONNECT: %w", err)
	}

	// Bound response headers. Only buffered bytes are read through this limited
	// reader after CONNECT; subsequent tunnel reads go straight to the socket.
	reader := bufio.NewReader(io.LimitReader(conn, 32*1024))
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		return nil, fmt.Errorf("read HTTP CONNECT response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP CONNECT rejected: status %d", response.StatusCode)
	}
	if !stop() || ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	ok = true
	return streamcopy.WithReadBuffer(conn.(*net.TCPConn), reader), nil
}
