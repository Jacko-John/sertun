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
)

type Dialer struct {
	proxy   *url.URL
	timeout time.Duration
}

func New(proxy string, timeout time.Duration) (*Dialer, error) {
	u, err := config.ParseProxy(proxy)
	if err != nil {
		return nil, err
	}
	return &Dialer{proxy: u, timeout: timeout}, nil
}

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
	if reader.Buffered() != 0 {
		return &bufferedConn{Conn: conn, reader: reader}, nil
	}
	return conn, nil
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	if c.reader.Buffered() > 0 {
		return c.reader.Read(p)
	}
	return c.Conn.Read(p)
}

func (c *bufferedConn) CloseWrite() error {
	return c.Conn.(interface{ CloseWrite() error }).CloseWrite()
}
