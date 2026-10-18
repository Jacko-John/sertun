// Package bench runs reproducible process-level comparisons outside the product
// forwarding path. It downloads pinned official tools into a verified cache.
package bench

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	frpVersion    = "v0.71.1"
	mihomoVersion = "v1.19.32"
)

type tools struct{ sertun, frpc, frps, mihomo string }

// ToolInfo records executable provenance without publishing local file paths.
type ToolInfo struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	SHA256    string `json:"sha256"`
	GoVersion string `json:"go_version"`
	Bytes     int64  `json:"bytes"`
}

type releaseAsset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Digest string `json:"digest"`
}

// prepareTools verifies cached executables or extracts checksum-verified assets.
func prepareTools(ctx context.Context) (tools, error) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return tools{}, fmt.Errorf("benchmark resource sampling currently supports macOS and Linux")
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return tools{}, err
	}
	cache = filepath.Join(cache, "sertun-bench")
	frpAsset := fmt.Sprintf("frp_%s_%s_%s.tar.gz", strings.TrimPrefix(frpVersion, "v"), runtime.GOOS, runtime.GOARCH)
	frpDir, err := cachedRelease(ctx, cache, "fatedier/frp", frpVersion, []string{frpAsset}, []string{"frpc", "frps"})
	if err != nil {
		return tools{}, err
	}
	mihomoNames := []string{fmt.Sprintf("mihomo-%s-%s-%s.gz", runtime.GOOS, runtime.GOARCH, mihomoVersion)}
	if runtime.GOOS == "linux" {
		suffix := map[string]string{"amd64": "amd64-v1", "arm64": "arm64-v8"}[runtime.GOARCH]
		if suffix != "" {
			mihomoNames = append(mihomoNames, fmt.Sprintf("mihomo-linux-%s-%s.gz", suffix, mihomoVersion))
		}
	}
	mihomoDir, err := cachedRelease(ctx, cache, "MetaCubeX/mihomo", mihomoVersion, mihomoNames, []string{"mihomo"})
	if err != nil {
		return tools{}, err
	}
	return tools{frpc: filepath.Join(frpDir, "frpc"), frps: filepath.Join(frpDir, "frps"), mihomo: filepath.Join(mihomoDir, "mihomo")}, nil
}

// cachedRelease validates cached hashes; incomplete caches are rebuilt in place.
func cachedRelease(ctx context.Context, cache, repo, version string, names, binaries []string) (string, error) {
	dir := filepath.Join(cache, strings.ReplaceAll(repo, "/", "-"), version, runtime.GOOS+"-"+runtime.GOARCH)
	manifest := filepath.Join(dir, "checksums.json")
	var hashes map[string]string
	if data, err := os.ReadFile(manifest); err == nil && json.Unmarshal(data, &hashes) == nil {
		valid := true
		for _, name := range binaries {
			hash, err := fileHash(filepath.Join(dir, name))
			valid = valid && err == nil && hash == hashes[name]
		}
		if valid {
			return dir, nil
		}
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	asset, err := findAsset(ctx, repo, version, names)
	if err != nil {
		return "", err
	}
	archive, err := downloadAsset(ctx, dir, asset)
	if err != nil {
		return "", err
	}
	defer os.Remove(archive)
	if err := extractTools(archive, dir, asset.Name, binaries); err != nil {
		return "", err
	}
	hashes = make(map[string]string)
	for _, name := range binaries {
		hash, err := fileHash(filepath.Join(dir, name))
		if err != nil {
			return "", err
		}
		hashes[name] = hash
	}
	data, _ := json.MarshalIndent(hashes, "", "  ")
	if err := os.WriteFile(manifest, data, 0600); err != nil {
		return "", err
	}
	return dir, nil
}

// findAsset selects an exact asset from a pinned official GitHub release.
func findAsset(ctx context.Context, repo, version string, names []string) (releaseAsset, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+repo+"/releases/tags/"+version, nil)
	if err != nil {
		return releaseAsset{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "sertun-bench")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return releaseAsset{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return releaseAsset{}, fmt.Errorf("read %s release: HTTP %d", repo, response.StatusCode)
	}
	var release struct{ Assets []releaseAsset }
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&release); err != nil {
		return releaseAsset{}, err
	}
	for _, name := range names {
		for _, asset := range release.Assets {
			if asset.Name == name {
				return asset, nil
			}
		}
	}
	return releaseAsset{}, fmt.Errorf("no %s asset for %s/%s", repo, runtime.GOOS, runtime.GOARCH)
}

// downloadAsset validates the published archive digest before any extraction.
func downloadAsset(ctx context.Context, dir string, asset releaseAsset) (string, error) {
	if !strings.HasPrefix(asset.Digest, "sha256:") {
		return "", fmt.Errorf("%s has no published SHA-256 digest", asset.Name)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return "", err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: HTTP %d", asset.Name, response.StatusCode)
	}
	f, err := os.CreateTemp(dir, ".download-")
	if err != nil {
		return "", err
	}
	path := f.Name()
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(f, hash), response.Body)
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil || hex.EncodeToString(hash.Sum(nil)) != strings.TrimPrefix(asset.Digest, "sha256:") {
		os.Remove(path)
		return "", fmt.Errorf("download or checksum verification failed for %s: %v / %v", asset.Name, copyErr, closeErr)
	}
	return path, nil
}

// extractTools extracts only the requested executables, never archive paths.
func extractTools(path, dir, name string, binaries []string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	if !strings.HasSuffix(name, ".tar.gz") {
		return writeExecutable(filepath.Join(dir, binaries[0]), gz)
	}
	reader := tar.NewReader(gz)
	found := make(map[string]bool)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			for _, binary := range binaries {
				if !found[binary] {
					return fmt.Errorf("release archive does not contain %s", binary)
				}
			}
			return nil
		}
		if err != nil {
			return err
		}
		for _, binary := range binaries {
			if header.Typeflag == tar.TypeReg && filepath.Base(header.Name) == binary {
				if err := writeExecutable(filepath.Join(dir, binary), reader); err != nil {
					return err
				}
				found[binary] = true
			}
		}
	}
}

// writeExecutable persists one verified tool and propagates copy/close failures.
func writeExecutable(path string, reader io.Reader) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, reader)
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// fileHash hashes a binary in a streaming pass without retaining it in memory.
func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// describeTool records the actual binary and compiler, including release builds.
func describeTool(name, version, path string) (ToolInfo, error) {
	hash, err := fileHash(path)
	if err != nil {
		return ToolInfo{}, err
	}
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return ToolInfo{}, err
	}
	stat, err := os.Stat(path)
	if err != nil {
		return ToolInfo{}, err
	}
	return ToolInfo{Name: name, Version: version, SHA256: hash, GoVersion: info.GoVersion, Bytes: stat.Size()}, nil
}
