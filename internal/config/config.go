// Package config loads and validates the TOML files used by both roles.
// LoadServer and LoadClient return configurations with defaults already
// applied, so callers do not repeat default handling.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Duration decodes a TOML string such as "10s" into a time.Duration.
type Duration time.Duration

// UnmarshalText parses the duration string used in both configuration files.
func (d *Duration) UnmarshalText(text []byte) error {
	value, err := time.ParseDuration(string(text))
	if err != nil {
		return err
	}
	*d = Duration(value)
	return nil
}

// Server describes the tunnel entry point and the clients it serves.
type Server struct {
	Listen           string         `toml:"listen"`
	Timeout          Duration       `toml:"timeout"`
	HeartbeatTimeout Duration       `toml:"heartbeat_timeout"`
	MaxHandshakes    int            `toml:"max_handshakes"`
	Clients          []ServerClient `toml:"clients"`
}

// ServerClient authorizes one client and bounds its pending and active work.
type ServerClient struct {
	ID             string          `toml:"id"`
	Token          string          `toml:"token"`
	MaxConnections int             `toml:"max_connections"`
	MaxPending     int             `toml:"max_pending"`
	Services       []ServerService `toml:"services"`
}

// ServerService maps a listening address to a service name the client serves.
type ServerService struct {
	Name   string `toml:"name"`
	Listen string `toml:"listen"`
}

// Client names the server and the local services this client exposes.
type Client struct {
	Server            string          `toml:"server"`
	ID                string          `toml:"id"`
	Token             string          `toml:"token"`
	Proxy             string          `toml:"proxy"`
	Timeout           Duration        `toml:"timeout"`
	HeartbeatInterval Duration        `toml:"heartbeat_interval"`
	HeartbeatTimeout  Duration        `toml:"heartbeat_timeout"`
	ReconnectMin      Duration        `toml:"reconnect_min"`
	ReconnectMax      Duration        `toml:"reconnect_max"`
	MaxConnections    int             `toml:"max_connections"`
	Services          []ClientService `toml:"services"`
}

// ClientService maps a service name to the local address the client dials.
type ClientService struct {
	Name  string `toml:"name"`
	Local string `toml:"local"`
}

// LoadServer reads path, applies defaults, and validates the configuration.
func LoadServer(path string) (Server, error) {
	var cfg Server
	if err := load(path, &cfg); err != nil {
		return cfg, err
	}
	return cfg, cfg.Validate()
}

// LoadClient reads path, applies defaults, and validates the configuration.
func LoadClient(path string) (Client, error) {
	var cfg Client
	if err := load(path, &cfg); err != nil {
		return cfg, err
	}
	return cfg, cfg.Validate()
}

// load decodes strictly so an unknown key fails instead of being ignored.
func load(path string, dst any) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open config: %w", err)
	}
	defer f.Close()
	if err := toml.NewDecoder(f).DisallowUnknownFields().Decode(dst); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}
	return nil
}

// Validate applies server defaults and checks identities, addresses, and
// resource limits. It is idempotent.
func (c *Server) Validate() error {
	if c.Listen == "" {
		c.Listen = "127.0.0.1:7000"
	}
	if err := address(c.Listen, true); err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defaultDuration(&c.Timeout, 10*time.Second)
	defaultDuration(&c.HeartbeatTimeout, 60*time.Second)
	if c.Timeout <= 0 || c.HeartbeatTimeout <= 0 {
		return fmt.Errorf("timeout and heartbeat_timeout must be positive")
	}
	if c.MaxHandshakes == 0 {
		c.MaxHandshakes = 64
	}
	if c.MaxHandshakes < 1 {
		return fmt.Errorf("max_handshakes must be positive")
	}
	if len(c.Clients) == 0 {
		return fmt.Errorf("at least one client is required")
	}

	ids := make(map[string]bool)
	for i := range c.Clients {
		client := &c.Clients[i]
		if err := identity(client.ID, client.Token); err != nil {
			return fmt.Errorf("client %d: %w", i+1, err)
		}
		if ids[client.ID] {
			return fmt.Errorf("duplicate client id %q", client.ID)
		}
		ids[client.ID] = true

		if client.MaxConnections == 0 {
			client.MaxConnections = 128
		}
		if client.MaxPending == 0 {
			client.MaxPending = min(32, client.MaxConnections)
		}
		if client.MaxConnections < 1 || client.MaxPending < 1 || client.MaxPending > client.MaxConnections {
			return fmt.Errorf("client %q: require 1 <= max_pending <= max_connections", client.ID)
		}

		names := make([]string, 0, len(client.Services))
		for _, service := range client.Services {
			names = append(names, service.Name)
		}
		if err := validateServiceNames(names); err != nil {
			return fmt.Errorf("client %q: %w", client.ID, err)
		}
		for _, service := range client.Services {
			if err := address(service.Listen, true); err != nil {
				return fmt.Errorf("service %q listen: %w", service.Name, err)
			}
		}
	}
	return nil
}

// Validate applies client defaults and checks the server address, proxy URL,
// timeouts, and service list. It is idempotent.
func (c *Client) Validate() error {
	if err := identity(c.ID, c.Token); err != nil {
		return err
	}
	if err := address(c.Server, false); err != nil {
		return fmt.Errorf("server: %w", err)
	}
	if _, err := ParseProxy(c.Proxy); err != nil {
		return err
	}
	defaultDuration(&c.Timeout, 10*time.Second)
	defaultDuration(&c.HeartbeatInterval, 20*time.Second)
	defaultDuration(&c.HeartbeatTimeout, 60*time.Second)
	defaultDuration(&c.ReconnectMin, time.Second)
	defaultDuration(&c.ReconnectMax, 30*time.Second)
	if c.Timeout <= 0 || c.HeartbeatInterval <= 0 || c.HeartbeatTimeout <= c.HeartbeatInterval {
		return fmt.Errorf("timeouts must be positive; heartbeat_timeout must exceed heartbeat_interval")
	}
	if c.ReconnectMin <= 0 || c.ReconnectMax < c.ReconnectMin {
		return fmt.Errorf("require 0 < reconnect_min <= reconnect_max")
	}

	if c.MaxConnections == 0 {
		c.MaxConnections = 128
	}
	if c.MaxConnections < 1 {
		return fmt.Errorf("max_connections must be positive")
	}

	names := make([]string, 0, len(c.Services))
	for _, service := range c.Services {
		names = append(names, service.Name)
	}
	if err := validateServiceNames(names); err != nil {
		return err
	}
	for _, service := range c.Services {
		if err := address(service.Local, false); err != nil {
			return fmt.Errorf("service %q local: %w", service.Name, err)
		}
	}
	return nil
}

// ParseProxy accepts only an http:// CONNECT proxy. An empty string means a
// direct connection; transport encryption belongs to the outer proxy.
func ParseProxy(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		// URL parser errors may contain embedded credentials.
		return nil, fmt.Errorf("invalid proxy URL")
	}
	valid := u.Scheme == "http" && u.Hostname() != "" && (u.Path == "" || u.Path == "/") && u.RawQuery == "" && u.Fragment == ""
	if !valid {
		return nil, fmt.Errorf("proxy must be an http://host:port CONNECT proxy URL")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("invalid proxy port")
		}
	}
	return u, nil
}

// validateServiceNames enforces the service count and naming rules shared by
// both roles.
func validateServiceNames(names []string) error {
	if len(names) == 0 || len(names) > 128 {
		return fmt.Errorf("require 1 to 128 services")
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if !validName(name) || seen[name] {
			return fmt.Errorf("invalid or duplicate service name %q", name)
		}
		seen[name] = true
	}
	return nil
}

// defaultDuration fills a zero duration with fallback.
func defaultDuration(value *Duration, fallback time.Duration) {
	if *value == 0 {
		*value = Duration(fallback)
	}
}

// identity checks the client ID and token rules shared by both roles.
func identity(id, token string) error {
	if !validName(id) {
		return fmt.Errorf("id must contain 1 to 64 ASCII letters, digits, '.', '_' or '-'")
	}
	if strings.TrimSpace(token) == "" || len(token) > 512 {
		return fmt.Errorf("token must contain 1 to 512 bytes")
	}
	return nil
}

// validName reports whether value is 1 to 64 ASCII letters, digits, '.', '_'
// or '-'.
func validName(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !isNameRune(r) {
			return false
		}
	}
	return true
}

// isNameRune reports whether r is allowed in a client ID or service name.
func isNameRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
}

// address checks host:port form. Client targets also require a host and a
// non-zero port; listeners may bind a wildcard host or an ephemeral port.
func address(value string, listen bool) error {
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return fmt.Errorf("expected host:port")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return fmt.Errorf("invalid port")
	}
	if !listen && (host == "" || n == 0) {
		return fmt.Errorf("client target requires a host and a non-zero port")
	}
	return nil
}
