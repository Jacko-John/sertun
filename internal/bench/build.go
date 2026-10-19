// Build helpers keep optimization comparisons on the same compiler and flags.
// Historical sources are exported into a disposable directory, never checked out.

package bench

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// buildSertun builds one production executable from an explicit source root.
func buildSertun(ctx context.Context, source, binary, version string) error {
	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags", "-s -w -X main.version="+version, "-o", binary, "./cmd/sertun")
	cmd.Dir = source
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("build sertun: %w: %s", err, output)
	}
	return nil
}

// buildBaseline resolves a Git commit and exports tracked regular files only.
func buildBaseline(ctx context.Context, root, ref string) (binary, commit string, err error) {
	resolved, err := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "--end-of-options", ref+"^{commit}").Output()
	if err != nil {
		return "", "", fmt.Errorf("resolve baseline Git commit: %w", err)
	}
	commit = strings.TrimSpace(string(resolved))
	archive, err := exec.CommandContext(ctx, "git", "archive", "--format=tar", commit).Output()
	if err != nil {
		return "", "", fmt.Errorf("export baseline source: %w", err)
	}
	source := filepath.Join(root, "baseline-source")
	if err := os.Mkdir(source, 0700); err != nil {
		return "", "", err
	}
	reader := tar.NewReader(bytes.NewReader(archive))
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", "", err
		}
		if !filepath.IsLocal(header.Name) {
			return "", "", fmt.Errorf("baseline archive contains a non-local path")
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		path := filepath.Join(source, header.Name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return "", "", err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return "", "", err
		}
		_, copyErr := io.Copy(f, reader)
		closeErr := f.Close()
		if copyErr != nil {
			return "", "", copyErr
		}
		if closeErr != nil {
			return "", "", closeErr
		}
	}
	binary = filepath.Join(root, "sertun-baseline")
	err = buildSertun(ctx, source, binary, commit)
	return binary, commit, err
}
