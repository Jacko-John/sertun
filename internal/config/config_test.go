// Tests cover the example files and strict unknown-field decoding.

package config

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestExampleConfigsAndStrictDecoding(t *testing.T) {
	if _, err := LoadServer(filepath.Join("..", "..", "examples", "server.toml")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadClient(filepath.Join("..", "..", "examples", "client.toml")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "client.toml")
	if err := os.WriteFile(path, []byte("proxy_typo = 'http://127.0.0.1:7890'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := load(path, &Client{}); err == nil {
		t.Fatal("unknown configuration key accepted")
	}
}

// FuzzConfigDecode keeps arbitrary TOML inside the decode and validation
// contracts. Validation must be safe on any successfully decoded config.
func FuzzConfigDecode(f *testing.F) {
	f.Add([]byte("server = \"127.0.0.1:7000\"\nid = \"home\"\ntoken = \"token\"\n[[services]]\nname = \"ssh\"\nlocal = \"127.0.0.1:22\"\n"))
	f.Add([]byte("listen = \"127.0.0.1:7000\"\n[[clients]]\nid = \"home\"\ntoken = \"token\"\n[[clients.services]]\nname = \"ssh\"\nlisten = \"127.0.0.1:60022\"\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		var client Client
		if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&client); err == nil {
			_ = client.Validate()
		}
		var server Server
		if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&server); err == nil {
			_ = server.Validate()
		}
	})
}
