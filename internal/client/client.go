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

var ErrRejected = errors.New("registration rejected")

type Client struct {
	cfg      config.Client
	log      *slog.Logger
	dialer   *dial.Dialer
	services map[string]string
}

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
		if time.Since(started) >= time.Duration(c.cfg.HeartbeatInterval) {
			backoff = time.Duration(c.cfg.ReconnectMin)
		}
		// Jitter only reconnect attempts; idle sessions have no polling loop.
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
	if err := conn.SetDeadline(time.Now().Add(time.Duration(c.cfg.Timeout))); err != nil {
		return err
	}
	services := make([]string, 0, len(c.cfg.Services))
	for _, service := range c.cfg.Services {
		services = append(services, service.Name)
	}
	if err := wire.Write(conn, wire.Message{Type: "login", Client: c.cfg.ID, Token: c.cfg.Token, Services: services}); err != nil {
		return err
	}
	reply, err := wire.Read(conn)
	if err != nil {
		return fmt.Errorf("read registration reply: %w", err)
	}
	if reply.Type == "error" {
		if reply.Code == "busy" {
			return fmt.Errorf("client is already connected")
		}
		return fmt.Errorf("%w: %s", ErrRejected, reply.Error)
	}
	if reply.Type != "login_ok" || reply.Session == "" {
		return fmt.Errorf("invalid registration reply")
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return err
	}
	c.log.Info("tunnel connected", "client", c.cfg.ID, "server", c.cfg.Server)
	sender := &wire.Sender{Conn: conn, Timeout: time.Duration(c.cfg.Timeout)}
	var workers sync.WaitGroup
	defer func() {
		cancel()
		conn.Close()
		workers.Wait()
	}()
	workers.Add(1)
	go func() {
		defer workers.Done()
		ticker := time.NewTicker(time.Duration(c.cfg.HeartbeatInterval))
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := sender.Send(wire.Message{Type: "ping"}); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	slots := make(chan struct{}, c.cfg.MaxConnections)
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
			local, exists := c.services[message.Service]
			if !exists || message.Request == "" {
				return fmt.Errorf("invalid service request")
			}
			select {
			case slots <- struct{}{}:
				workers.Add(1)
				go func() {
					defer workers.Done()
					defer func() { <-slots }()
					if err := c.forward(ctx, local, reply.Session, message.Request); err != nil && ctx.Err() == nil {
						c.log.Debug("service connection ended", "service", message.Service, "error", err)
						if err := sender.Send(wire.Message{Type: "open_failed", Request: message.Request}); err != nil {
							cancel()
						}
					}
				}()
			default:
				if err := sender.Send(wire.Message{Type: "open_failed", Request: message.Request}); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("unexpected control message")
		}
	}
}

func (c *Client) forward(ctx context.Context, address, session, request string) error {
	setupCtx, cancel := context.WithTimeout(ctx, time.Duration(c.cfg.Timeout))
	defer cancel()
	dialer := &net.Dialer{KeepAlive: 30 * time.Second}
	local, err := dialer.DialContext(setupCtx, "tcp", address)
	if err != nil {
		return fmt.Errorf("connect to local service: %w", err)
	}
	defer local.Close()
	work, err := c.dialer.DialContext(setupCtx, c.cfg.Server)
	if err != nil {
		return err
	}
	defer work.Close()
	stop := context.AfterFunc(setupCtx, func() { work.Close() })
	defer stop()
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
	if !stop() || setupCtx.Err() != nil {
		return setupCtx.Err()
	}
	if err := work.SetDeadline(time.Time{}); err != nil {
		return err
	}
	return relay.Bridge(ctx, local, work)
}
