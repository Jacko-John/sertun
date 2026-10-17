package config

import (
	"os"
	"path/filepath"
	"testing"
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
