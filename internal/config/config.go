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

type Duration time.Duration

func (d *Duration) UnmarshalText(text []byte) error {
	value, err := time.ParseDuration(string(text))
	if err != nil {
		return err
	}
	*d = Duration(value)
	return nil
}

type Server struct {
	Listen           string         `toml:"listen"`
	Timeout          Duration       `toml:"timeout"`
	HeartbeatTimeout Duration       `toml:"heartbeat_timeout"`
	MaxHandshakes    int            `toml:"max_handshakes"`
	Clients          []ServerClient `toml:"clients"`
}

type ServerClient struct {
	ID             string          `toml:"id"`
	Token          string          `toml:"token"`
	MaxConnections int             `toml:"max_connections"`
	MaxPending     int             `toml:"max_pending"`
	Services       []ServerService `toml:"services"`
}

type ServerService struct {
	Name   string `toml:"name"`
	Listen string `toml:"listen"`
}

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

type ClientService struct {
	Name  string `toml:"name"`
	Local string `toml:"local"`
}

func LoadServer(path string) (Server, error) {
	var cfg Server
	if err := load(path, &cfg); err != nil {
		return cfg, err
	}
	return cfg, cfg.Validate()
}

func LoadClient(path string) (Client, error) {
	var cfg Client
	if err := load(path, &cfg); err != nil {
		return cfg, err
	}
	return cfg, cfg.Validate()
}

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
		names := make(map[string]bool)
		if len(client.Services) == 0 || len(client.Services) > 128 {
			return fmt.Errorf("client %q: require 1 to 128 services", client.ID)
		}
		for _, service := range client.Services {
			if !validName(service.Name) || names[service.Name] {
				return fmt.Errorf("client %q: invalid or duplicate service name %q", client.ID, service.Name)
			}
			names[service.Name] = true
			if err := address(service.Listen, true); err != nil {
				return fmt.Errorf("service %q listen: %w", service.Name, err)
			}
		}
	}
	return nil
}

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
	if len(c.Services) == 0 || len(c.Services) > 128 {
		return fmt.Errorf("require 1 to 128 services")
	}
	names := make(map[string]bool)
	for _, service := range c.Services {
		if !validName(service.Name) || names[service.Name] {
			return fmt.Errorf("invalid or duplicate service name %q", service.Name)
		}
		names[service.Name] = true
		if err := address(service.Local, false); err != nil {
			return fmt.Errorf("service %q local: %w", service.Name, err)
		}
	}
	return nil
}

// ParseProxy deliberately accepts HTTP CONNECT only; transport encryption belongs
// to the independently running outer proxy.
func ParseProxy(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		// URL parser errors may contain embedded credentials.
		return nil, fmt.Errorf("invalid proxy URL")
	}
	if u.Scheme != "http" || u.Hostname() == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
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

func defaultDuration(value *Duration, fallback time.Duration) {
	if *value == 0 {
		*value = Duration(fallback)
	}
}

func identity(id, token string) error {
	if !validName(id) {
		return fmt.Errorf("id must contain 1 to 64 ASCII letters, digits, '.', '_' or '-'")
	}
	if strings.TrimSpace(token) == "" || len(token) > 512 {
		return fmt.Errorf("token must contain 1 to 512 bytes")
	}
	return nil
}

func validName(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func address(value string, listen bool) error {
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return fmt.Errorf("expected host:port")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 || (!listen && (host == "" || n == 0)) {
		return fmt.Errorf("invalid host or port")
	}
	return nil
}
