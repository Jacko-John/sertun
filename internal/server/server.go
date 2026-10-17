package server

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"sertun/internal/config"
	"sertun/internal/relay"
	"sertun/internal/wire"
)

type Server struct {
	cfg     config.Server
	log     *slog.Logger
	clients map[string]config.ServerClient

	mu       sync.Mutex
	sessions map[string]*session
	pending  map[string]*pending
	wg       sync.WaitGroup
	slots    chan struct{}

	control   net.Listener
	listeners []serviceListener
	ready     chan struct{}
}

type serviceListener struct {
	net.Listener
	client  string
	service string
}

type session struct {
	id      string
	client  string
	ctx     context.Context
	cancel  context.CancelFunc
	sender  *wire.Sender
	ready   bool // Protected, like the counters, by Server.mu.
	active  int
	waiting int
}

type pending struct {
	id      string
	service string
	session *session
	ctx     context.Context
	cancel  context.CancelFunc
	conn    chan net.Conn // Unbuffered: a timed-out receiver cannot orphan a socket.
}

func New(cfg config.Server, logger *slog.Logger) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{
		cfg: cfg, log: logger,
		clients:  make(map[string]config.ServerClient),
		sessions: make(map[string]*session), pending: make(map[string]*pending),
		slots: make(chan struct{}, cfg.MaxHandshakes), ready: make(chan struct{}),
	}
	for _, client := range cfg.Clients {
		s.clients[client.ID] = client
	}
	return s, nil
}

// Run binds the configured ports and blocks until cancellation or a listener
// failure. A Server is used for one Run; reconnects reuse its mapped listeners.
func (s *Server) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	lc := &net.ListenConfig{KeepAlive: 30 * time.Second}
	var err error
	s.control, err = lc.Listen(ctx, "tcp", s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen for tunnels: %w", err)
	}
	closeListeners := func() {
		s.control.Close()
		for _, listener := range s.listeners {
			listener.Close()
		}
	}
	defer closeListeners()
	for _, client := range s.cfg.Clients {
		for _, service := range client.Services {
			listener, err := lc.Listen(ctx, "tcp", service.Listen)
			if err != nil {
				return fmt.Errorf("listen for %s/%s: %w", client.ID, service.Name, err)
			}
			s.listeners = append(s.listeners, serviceListener{listener, client.ID, service.Name})
		}
	}
	stop := context.AfterFunc(ctx, closeListeners)
	defer stop()
	failures := make(chan error, 1)
	fail := func(err error) {
		select {
		case failures <- err:
		default:
		}
		cancel()
	}
	for _, listener := range s.listeners {
		s.log.Info("mapping listening", "client", listener.client, "service", listener.service, "listen", listener.Addr())
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			if err := s.acceptService(ctx, listener); err != nil && ctx.Err() == nil {
				fail(err)
			}
		}()
	}
	s.log.Info("tunnel listening", "listen", s.control.Addr())
	close(s.ready)
	if err := s.acceptTunnels(ctx); err != nil && ctx.Err() == nil {
		fail(err)
	}
	cancel()
	s.wg.Wait()
	select {
	case err := <-failures:
		return err
	default:
		return nil
	}
}

func (s *Server) acceptTunnels(ctx context.Context) error {
	for {
		conn, err := s.control.Accept()
		if err != nil {
			return fmt.Errorf("accept tunnel: %w", err)
		}
		select {
		case s.slots <- struct{}{}:
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.handleTunnel(ctx, conn)
			}()
		default:
			conn.Close()
		}
	}
}

func (s *Server) handleTunnel(ctx context.Context, conn net.Conn) {
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { <-s.slots }) }
	defer release()
	owned := true
	defer func() {
		if owned {
			conn.Close()
		}
	}()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if err := conn.SetDeadline(time.Now().Add(time.Duration(s.cfg.Timeout))); err != nil {
		return
	}
	message, err := wire.Read(conn)
	if err != nil {
		return
	}
	switch message.Type {
	case "login":
		sess := s.login(ctx, conn, message)
		if sess == nil {
			return
		}
		release()
		s.controlSession(conn, sess)
	case "attach":
		owned = !s.attach(conn, message)
	default:
		s.reject(conn, "bad_request", "expected login or attach")
	}
}

func (s *Server) login(ctx context.Context, conn net.Conn, message wire.Message) *session {
	client, exists := s.clients[message.Client]
	if !exists || subtle.ConstantTimeCompare([]byte(client.Token), []byte(message.Token)) != 1 {
		s.reject(conn, "denied", "authentication rejected")
		return nil
	}
	names := make(map[string]bool)
	for _, service := range client.Services {
		names[service.Name] = true
	}
	if len(message.Services) != len(names) {
		s.reject(conn, "bad_services", "registered services must match server configuration")
		return nil
	}
	for _, name := range message.Services {
		if !names[name] {
			s.reject(conn, "bad_services", "registered services must match server configuration")
			return nil
		}
		delete(names, name)
	}
	id, err := wire.NewID()
	if err != nil {
		return nil
	}
	sessCtx, cancel := context.WithCancel(ctx)
	sess := &session{
		id: id, client: client.ID, ctx: sessCtx, cancel: cancel,
		sender: &wire.Sender{Conn: conn, Timeout: time.Duration(s.cfg.Timeout)},
	}
	s.mu.Lock()
	if s.sessions[client.ID] != nil {
		s.mu.Unlock()
		cancel()
		s.reject(conn, "busy", "client is already connected")
		return nil
	}
	s.sessions[client.ID] = sess
	s.mu.Unlock()
	// The session remains unavailable to visitors until its login reply is sent.
	if err := sess.sender.Send(wire.Message{Type: "login_ok", Session: id}); err != nil {
		s.endSession(sess)
		return nil
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		s.endSession(sess)
		return nil
	}
	s.mu.Lock()
	sess.ready = true
	s.mu.Unlock()
	s.log.Info("client connected", "client", client.ID)
	return sess
}

func (s *Server) controlSession(conn net.Conn, sess *session) {
	defer s.endSession(sess)
	stop := context.AfterFunc(sess.ctx, func() { conn.Close() })
	defer stop()
	for {
		if err := conn.SetReadDeadline(time.Now().Add(time.Duration(s.cfg.HeartbeatTimeout))); err != nil {
			return
		}
		message, err := wire.Read(conn)
		if err != nil {
			return
		}
		switch message.Type {
		case "ping":
			if err := sess.sender.Send(wire.Message{Type: "pong"}); err != nil {
				return
			}
		case "open_failed":
			s.mu.Lock()
			if p := s.pending[message.Request]; p != nil && p.session == sess {
				delete(s.pending, p.id)
				sess.waiting--
				p.cancel()
			}
			s.mu.Unlock()
		default:
			return
		}
	}
}

func (s *Server) endSession(sess *session) {
	s.mu.Lock()
	if s.sessions[sess.client] == sess {
		delete(s.sessions, sess.client)
	}
	s.mu.Unlock()
	sess.cancel()
	s.log.Info("client disconnected", "client", sess.client)
}

func (s *Server) acceptService(ctx context.Context, listener serviceListener) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return fmt.Errorf("accept %s/%s: %w", listener.client, listener.service, err)
		}
		p := s.reserve(listener.client, listener.service)
		if p == nil {
			conn.Close()
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.forward(ctx, conn, p)
		}()
	}
}

func (s *Server) reserve(client, service string) *pending {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[client]
	limits := s.clients[client]
	if sess == nil || !sess.ready || sess.ctx.Err() != nil || sess.active >= limits.MaxConnections || sess.waiting >= limits.MaxPending {
		return nil
	}
	id, err := wire.NewID()
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(sess.ctx, time.Duration(s.cfg.Timeout))
	p := &pending{id: id, service: service, session: sess, ctx: ctx, cancel: cancel, conn: make(chan net.Conn)}
	s.pending[id] = p
	sess.active++
	sess.waiting++
	return p
}

func (s *Server) forward(ctx context.Context, visitor net.Conn, p *pending) {
	defer visitor.Close()
	defer p.cancel()
	defer func() {
		s.mu.Lock()
		if s.pending[p.id] == p {
			delete(s.pending, p.id)
			p.session.waiting--
		}
		p.session.active--
		s.mu.Unlock()
	}()
	stop := context.AfterFunc(ctx, func() { visitor.Close() })
	defer stop()
	if err := p.session.sender.Send(wire.Message{Type: "open", Request: p.id, Service: p.service}); err != nil {
		p.session.cancel()
		return
	}
	select {
	case work := <-p.conn:
		p.cancel()
		if err := relay.Bridge(p.session.ctx, visitor, work); err != nil && p.session.ctx.Err() == nil {
			s.log.Debug("forwarding ended", "client", p.session.client, "service", p.service, "error", err)
		}
	case <-p.ctx.Done():
	}
}

func (s *Server) attach(conn net.Conn, message wire.Message) bool {
	s.mu.Lock()
	p := s.pending[message.Request]
	if p == nil || p.session.client != message.Client || p.session.id != message.Session || p.ctx.Err() != nil {
		s.mu.Unlock()
		s.reject(conn, "denied", "unknown or expired request")
		return false
	}
	delete(s.pending, p.id)
	p.session.waiting--
	s.mu.Unlock()
	if err := wire.Write(conn, wire.Message{Type: "ready"}); err != nil {
		p.cancel()
		return false
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		p.cancel()
		return false
	}
	select {
	case p.conn <- conn:
		return true
	case <-p.ctx.Done():
		return false
	}
}

func (s *Server) reject(conn net.Conn, code, reason string) {
	_ = wire.Write(conn, wire.Message{Type: "error", Code: code, Error: reason})
}
