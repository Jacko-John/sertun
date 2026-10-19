// Package client keeps one control session to the server, reconnects with
// bounded backoff, and bridges each requested service to its local address.
package client

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"sync"
	"time"

	"sertun/internal/config"
	"sertun/internal/dial"
	"sertun/internal/relay"
	"sertun/internal/wire"
)

// ErrRejected marks a registration the server will not accept on retry, such
// as a bad token or a service list that does not match the server.
var ErrRejected = errors.New("registration rejected")

// Client owns the reconnect loop and the local service map.
type Client struct {
	cfg      config.Client
	log      *slog.Logger
	dialer   *dial.Dialer
	services map[string]string
}

// sessionWork groups the state shared by one control loop and its work
// connections. The loop adds to group and waits only after it stops.
type sessionWork struct {
	slots  chan struct{}
	group  sync.WaitGroup
	sender *wire.Sender
	cancel context.CancelFunc
}

// New validates cfg and builds the proxy dialer used by control and work
// connections. Local service dials stay direct.
func New(cfg config.Client, logger *slog.Logger) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	dialer, err := dial.New(cfg.Proxy, time.Duration(cfg.Timeout))
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	c := &Client{cfg: cfg, log: logger, dialer: dialer, services: make(map[string]string)}
	for _, service := range cfg.Services {
		c.services[service.Name] = service.Local
	}
	return c, nil
}

// Run keeps one control session alive and reconnects with jittered backoff.
// A registration rejection is final; network failures and duplicate sessions
// are retried.
func (c *Client) Run(ctx context.Context) error {
	backoff := time.Duration(c.cfg.ReconnectMin)
	for ctx.Err() == nil {
		started := time.Now()
		err := c.runSession(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, ErrRejected) {
			return err
		}
		// A session that lived long enough resets the delay, so a healthy link
		// that drops once reconnects quickly.
		if time.Since(started) >= time.Duration(c.cfg.HeartbeatInterval) {
			backoff = time.Duration(c.cfg.ReconnectMin)
		}
		// Only reconnect attempts get jitter; an idle session has no polling loop.
		delay := time.Duration(float64(backoff) * (0.8 + rand.Float64()*0.4))
		c.log.Warn("tunnel unavailable; reconnecting", "error", err, "retry_in", delay)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		backoff = min(backoff*2, time.Duration(c.cfg.ReconnectMax))
	}
	return nil
}

// runSession registers once, serves control messages, and returns on the first
// failure. Every goroutine it starts is joined before it returns.
func (c *Client) runSession(ctx context.Context) error {
	conn, err := c.dialer.DialContext(ctx, c.cfg.Server)
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	session, err := c.register(conn)
	if err != nil {
		return err
	}
	c.log.Info("tunnel connected", "client", c.cfg.ID, "server", c.cfg.Server)

	work := &sessionWork{
		slots:  make(chan struct{}, c.cfg.MaxConnections),
		sender: &wire.Sender{Conn: conn, Timeout: time.Duration(c.cfg.Timeout)},
		cancel: cancel,
	}
	// Join the heartbeat and every work connection before returning. Canceling
	// first gives them a chance to exit.
	defer func() {
		cancel()
		conn.Close()
		work.group.Wait()
	}()
	work.group.Add(1)
	go func() {
		defer work.group.Done()
		if err := c.heartbeat(ctx, work.sender); err != nil {
			cancel()
		}
	}()

	// Read control messages until the peer, the proxy, or a deadline ends the
	// session. Work connections run independently within the same context.
	for {
		if err := conn.SetReadDeadline(time.Now().Add(time.Duration(c.cfg.HeartbeatTimeout))); err != nil {
			return err
		}
		message, err := wire.Read(conn)
		if err != nil {
			return fmt.Errorf("read control message: %w", err)
		}
		switch message.Type {
		case "pong":
		case "open":
			if err := c.handleOpen(ctx, message, session, work); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unexpected control message")
		}
	}
}

// register performs the login exchange and returns the session identifier. The
// handshake deadline is cleared before returning.
func (c *Client) register(conn net.Conn) (string, error) {
	if err := conn.SetDeadline(time.Now().Add(time.Duration(c.cfg.Timeout))); err != nil {
		return "", err
	}
	// The server compares the registered names as a set with its own list.
	services := make([]string, 0, len(c.cfg.Services))
	for _, service := range c.cfg.Services {
		services = append(services, service.Name)
	}
	if err := wire.Write(conn, wire.Message{Type: "login", Client: c.cfg.ID, Token: c.cfg.Token, Services: services}); err != nil {
		return "", err
	}
	reply, err := wire.Read(conn)
	if err != nil {
		return "", fmt.Errorf("read registration reply: %w", err)
	}
	if reply.Type == "error" {
		if reply.Code == "busy" {
			return "", fmt.Errorf("client is already connected")
		}
		return "", fmt.Errorf("%w: %s", ErrRejected, reply.Error)
	}
	if reply.Type != "login_ok" || reply.Session == "" {
		return "", fmt.Errorf("invalid registration reply")
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return "", err
	}
	return reply.Session, nil
}

// heartbeat sends pings until ctx ends or a write fails.
func (c *Client) heartbeat(ctx context.Context, sender *wire.Sender) error {
	ticker := time.NewTicker(time.Duration(c.cfg.HeartbeatInterval))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := sender.Send(wire.Message{Type: "ping"}); err != nil {
				return err
			}
		}
	}
}

// handleOpen starts one work connection or asks the server to fail the request.
func (c *Client) handleOpen(ctx context.Context, message wire.Message, session string, work *sessionWork) error {
	local, exists := c.services[message.Service]
	if !exists || message.Request == "" {
		return fmt.Errorf("invalid service request")
	}
	// One work connection per request. At the limit, fail this request instead
	// of dropping the control session.
	select {
	case work.slots <- struct{}{}:
		work.group.Add(1)
		go func() {
			defer work.group.Done()
			defer func() { <-work.slots }()
			if err := c.forward(ctx, local, session, message.Request); err != nil && ctx.Err() == nil {
				c.log.Debug("service connection ended", "service", message.Service, "error", err)
				if err := work.sender.Send(wire.Message{Type: "open_failed", Request: message.Request}); err != nil {
					work.cancel()
				}
			}
		}()
		return nil
	default:
		return work.sender.Send(wire.Message{Type: "open_failed", Request: message.Request})
	}
}

// forward dials the local service and a fresh work connection, attaches them,
// and relays until either side closes.
func (c *Client) forward(ctx context.Context, address, session, request string) error {
	setupCtx, cancel := context.WithTimeout(ctx, time.Duration(c.cfg.Timeout))
	defer cancel()

	// The local service is reached directly, never through the proxy.
	dialer := &net.Dialer{KeepAlive: 30 * time.Second}
	local, err := dialer.DialContext(setupCtx, "tcp", address)
	if err != nil {
		return fmt.Errorf("connect to local service: %w", err)
	}
	defer local.Close()

	// The work connection follows the same proxy path as the control session.
	work, err := c.dialer.DialContext(setupCtx, c.cfg.Server)
	if err != nil {
		return err
	}
	defer work.Close()
	stop := context.AfterFunc(setupCtx, func() { work.Close() })
	defer stop()

	// setupCtx always has a deadline.
	deadline, _ := setupCtx.Deadline()
	if err := work.SetDeadline(deadline); err != nil {
		return err
	}
	if err := wire.Write(work, wire.Message{Type: "attach", Client: c.cfg.ID, Session: session, Request: request}); err != nil {
		return err
	}
	reply, err := wire.Read(work)
	if err != nil {
		return fmt.Errorf("pair work connection: %w", err)
	}
	if reply.Type != "ready" {
		return fmt.Errorf("work connection rejected")
	}
	// Attach must finish inside the setup timeout. Once paired, only ctx
	// governs the connection.
	if !stop() || setupCtx.Err() != nil {
		return setupCtx.Err()
	}
	if err := work.SetDeadline(time.Time{}); err != nil {
		return err
	}
	return relay.Bridge(ctx, local, work)
}
