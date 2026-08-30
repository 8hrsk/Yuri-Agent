package desktop

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/browser"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const maxOpenTargetLength = 4096

// OpenExternalURL handles an explicit owner click from rendered Markdown. It
// accepts only ordinary web URLs; model-authored javascript/data/file schemes
// never reach the OS browser.
func (b *Bridge) OpenExternalURL(rawURL string) error {
	target, err := validateExternalURL(rawURL)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if b != nil {
		b.mu.RLock()
		if b.appCtx != nil {
			ctx = b.appCtx
		}
		b.mu.RUnlock()
	}
	wailsruntime.BrowserOpenURL(ctx, target)
	return nil
}

// OpenLocalPath opens an existing regular file or directory after resolving
// symlinks. The path comes from untrusted model output, but execution happens
// only after the owner clicks it and never passes through a shell.
func (b *Bridge) OpenLocalPath(path string) error {
	target, err := resolveOpenableLocalPath(path)
	if err != nil {
		return err
	}
	if err := browser.OpenFile(target); err != nil {
		return fmt.Errorf("open local path: %w", err)
	}
	return nil
}

func validateExternalURL(rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" || len(rawURL) > maxOpenTargetLength || containsControl(rawURL) {
		return "", fmt.Errorf("invalid external URL")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse external URL: %w", err)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return "", fmt.Errorf("only HTTP(S) links without embedded credentials are supported")
	}
	return parsed.String(), nil
}

func resolveOpenableLocalPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || len(path) > maxOpenTargetLength || containsControl(path) || !filepath.IsAbs(path) {
		return "", fmt.Errorf("local link must be an absolute path")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve local link: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect local link: %w", err)
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return "", fmt.Errorf("local link must target a regular file or directory")
	}
	return resolved, nil
}

func containsControl(value string) bool {
	return strings.IndexFunc(value, func(char rune) bool { return char < 0x20 || char == 0x7f }) >= 0
}
