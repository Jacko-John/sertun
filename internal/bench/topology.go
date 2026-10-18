// Topology generation uses minimal explicit settings and ephemeral credentials.
// Both compared tools reach exactly the same isolated outer proxy topology.
package bench

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"sertun/internal/wire"
)

// startTunnel starts separate production client/server executables. Only frp's
// multiplexing flag differs between its plain and mux variants.
func startTunnel(ctx context.Context, dir, variant string, control, exposed int, backend, proxy string, binaries tools, children *processes) error {
	token, err := wire.NewID()
	if err != nil {
		return err
	}
	var serverConfig, clientConfig, serverBin, clientBin string
	var serverArgs, clientArgs []string
	if variant == "sertun" || variant == "sertun-baseline" {
		serverConfig = fmt.Sprintf(`listen = "127.0.0.1:%d"
[[clients]]
id = "bench"
token = %q
[[clients.services]]
name = "echo"
listen = "127.0.0.1:%d"
`, control, token, exposed)
		clientConfig = fmt.Sprintf(`server = "127.0.0.1:%d"
id = "bench"
token = %q
proxy = %q
[[services]]
name = "echo"
local = %q
`, control, token, proxy, backend)
		serverBin, clientBin = binaries.sertun, binaries.sertun
		if variant == "sertun-baseline" {
			serverBin, clientBin = binaries.baseline, binaries.baseline
		}
		serverArgs, clientArgs = []string{"server"}, []string{"client"}
	} else {
		mux := variant == "frp-mux"
		_, backendPort, err := net.SplitHostPort(backend)
		if err != nil {
			return err
		}
		serverConfig = fmt.Sprintf(`bindAddr = "127.0.0.1"
bindPort = %d
proxyBindAddr = "127.0.0.1"
auth.token = %q
transport.tcpMux = %t
transport.tls.force = false
log.level = "error"
`, control, token, mux)
		clientConfig = fmt.Sprintf(`serverAddr = "127.0.0.1"
serverPort = %d
auth.token = %q
loginFailExit = false
transport.protocol = "tcp"
transport.tcpMux = %t
transport.poolCount = 0
transport.tls.enable = false
transport.proxyURL = %q
log.level = "error"
[[proxies]]
name = "echo"
type = "tcp"
localIP = "127.0.0.1"
localPort = %s
remotePort = %d
transport.useEncryption = false
transport.useCompression = false
`, control, token, mux, proxy, backendPort, exposed)
		serverBin, clientBin = binaries.frps, binaries.frpc
	}
	serverPath, clientPath := filepath.Join(dir, "server.toml"), filepath.Join(dir, "client.toml")
	if err := os.WriteFile(serverPath, []byte(serverConfig), 0600); err != nil {
		return err
	}
	if err := os.WriteFile(clientPath, []byte(clientConfig), 0600); err != nil {
		return err
	}
	serverArgs = append(serverArgs, "-c", serverPath)
	clientArgs = append(clientArgs, "-c", clientPath)
	server, err := startProcess(dir, "tunnel-server", serverBin, serverArgs...)
	if err != nil {
		return err
	}
	children.tunnel = append(children.tunnel, server)
	if err := waitPort(ctx, fmt.Sprintf("127.0.0.1:%d", control), children); err != nil {
		return err
	}
	client, err := startProcess(dir, "tunnel-client", clientBin, clientArgs...)
	if err == nil {
		children.tunnel = append(children.tunnel, client)
	}
	return err
}

// startProxy uses a single DIRECT process or two actual protocol endpoints.
// JSON is valid YAML and needs no additional configuration dependency.
func startProxy(ctx context.Context, dir, scenario string, httpPort, edgePort int, binaries tools, children *processes) (string, error) {
	if scenario == "direct" {
		return "", nil
	}
	base := func() map[string]any {
		return map[string]any{
			"mode": "rule", "log-level": "error", "ipv6": false,
			"find-process-mode": "off", "geo-auto-update": false,
			"profile": map[string]any{"store-selected": false},
			"rules":   []string{"MATCH,DIRECT"},
		}
	}
	client := base()
	client["port"], client["bind-address"] = httpPort, "127.0.0.1"
	if scenario != "mihomo-direct" {
		password, err := wire.NewID()
		if err != nil {
			return "", err
		}
		credentialDir := filepath.Join(dir, "proxy-server")
		if err := os.MkdirAll(credentialDir, 0700); err != nil {
			return "", err
		}
		certificate, key, err := makeCertificate(credentialDir)
		if err != nil {
			return "", err
		}
		protocol := "anytls"
		if scenario == "mihomo-hy2" {
			protocol = "hysteria2"
		}
		server := base()
		server["listeners"] = []any{map[string]any{
			"name": "bench-in", "type": protocol, "listen": "127.0.0.1", "port": edgePort,
			"users": map[string]string{"bench": password}, "proxy": "DIRECT",
			"certificate": certificate, "private-key": key,
		}}
		if err := startMihomo(dir, "proxy-server", server, binaries, children); err != nil {
			return "", err
		}
		if protocol == "anytls" {
			if err := waitPort(ctx, fmt.Sprintf("127.0.0.1:%d", edgePort), children); err != nil {
				return "", err
			}
		}
		client["proxies"] = []any{map[string]any{
			"name": "bench-out", "type": protocol, "server": "127.0.0.1", "port": edgePort,
			"password": password, "sni": "localhost", "skip-cert-verify": true,
		}}
		client["rules"] = []string{"MATCH,bench-out"}
	}
	if err := startMihomo(dir, "proxy-client", client, binaries, children); err != nil {
		return "", err
	}
	address := "127.0.0.1:" + strconv.Itoa(httpPort)
	if err := waitPort(ctx, address, children); err != nil {
		return "", err
	}
	return "http://" + address, nil
}

// startMihomo gives each instance its own home, preventing user config access.
func startMihomo(dir, name string, config map[string]any, binaries tools, children *processes) error {
	home := filepath.Join(dir, name)
	if err := os.MkdirAll(home, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(config)
	if err != nil {
		return err
	}
	path := filepath.Join(home, "config.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		return err
	}
	p, err := startProcess(dir, name, binaries.mihomo, "-d", home, "-f", path)
	if err == nil {
		children.proxy = append(children.proxy, p)
	}
	return err
}

// makeCertificate generates a lab-only ECDSA certificate. The loopback client
// skips trust verification; TLS encryption remains enabled in the protocol.
func makeCertificate(dir string) (string, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return "", "", err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", "", err
	}
	certificate, privateKey := filepath.Join(dir, "certificate.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(privateKey, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		return "", "", err
	}
	return certificate, privateKey, nil
}
