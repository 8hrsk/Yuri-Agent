package tools

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/security"
)

type staticWebResolver struct {
	addresses map[string][]net.IPAddr
	calls     atomic.Int32
}

func (resolver *staticWebResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	resolver.calls.Add(1)
	addresses, ok := resolver.addresses[host]
	if !ok {
		return nil, errors.New("host not found")
	}
	return append([]net.IPAddr(nil), addresses...), nil
}

func webFetchPolicy(t *testing.T, values ...string) domain.PolicyEngine {
	t.Helper()
	grantID, err := domain.NewID("grant")
	if err != nil {
		t.Fatal(err)
	}
	return security.NewPolicyEvaluator(security.WithPolicyGrants([]domain.PermissionGrant{{
		ID: grantID, SubjectID: "web-test", Capability: domain.CapabilityNetworkHTTP,
		Scope: domain.CapabilityScope{Kind: domain.ScopeNetwork, Values: values}, GrantedAt: time.Now().Add(-time.Minute),
	}}))
}

func staticHTTPDial(response string, calls *atomic.Int32) func(context.Context, string, string) (net.Conn, error) {
	return func(_ context.Context, _, _ string) (net.Conn, error) {
		calls.Add(1)
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			reader := bufio.NewReader(server)
			for {
				line, err := reader.ReadString('\n')
				if err != nil || line == "\r\n" {
					break
				}
			}
			_, _ = io.WriteString(server, response)
		}()
		return client, nil
	}
}

func TestWebFetchReturnsBoundedReadableHTML(t *testing.T) {
	resolver := &staticWebResolver{addresses: map[string][]net.IPAddr{
		"public.example": {{IP: net.ParseIP("93.184.216.34")}},
	}}
	var dialCalls atomic.Int32
	body := `<html><head><title> Yuri &amp; Web </title><script>secret()</script></head><body><h1>Заголовок</h1><p>Первый <strong>абзац</strong>.</p><style>.hidden{}</style><p>Второй.</p></body></html>`
	response := "HTTP/1.1 200 OK\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: " + stringInt(len(body)) + "\r\nConnection: close\r\n\r\n" + body
	tool, err := NewWebFetch(WebFetchConfig{
		Policy: webFetchPolicy(t, "*"), SubjectID: "web-test", Resolver: resolver,
		DialContext: staticHTTPDial(response, &dialCalls), Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := tool.Execute(context.Background(), WebFetchRequest{URL: "http://public.example/article#section"})
	if err != nil {
		t.Fatal(err)
	}
	if result.URL != "http://public.example/article" || result.FinalURL != result.URL {
		t.Fatalf("unexpected URLs: %#v", result)
	}
	if result.Title != "Yuri & Web" {
		t.Fatalf("unexpected title %q", result.Title)
	}
	if result.Content != "Заголовок\nПервый абзац.\nВторой." {
		t.Fatalf("unexpected extracted content %q", result.Content)
	}
	if strings.Contains(result.Content, "secret") || strings.Contains(result.Content, "hidden") {
		t.Fatalf("script/style leaked into content: %q", result.Content)
	}
	if result.Truncated || dialCalls.Load() != 1 || resolver.calls.Load() != 1 {
		t.Fatalf("unexpected fetch state: result=%#v dial=%d resolve=%d", result, dialCalls.Load(), resolver.calls.Load())
	}
}

func TestWebFetchRejectsPrivateAndMixedDNSBeforeDial(t *testing.T) {
	for name, addresses := range map[string][]net.IPAddr{
		"literal metadata": {{IP: net.ParseIP("169.254.169.254")}},
		"private DNS":      {{IP: net.ParseIP("10.0.0.8")}},
		"mixed DNS":        {{IP: net.ParseIP("93.184.216.34")}, {IP: net.ParseIP("127.0.0.1")}},
		"documentation":    {{IP: net.ParseIP("203.0.113.7")}},
	} {
		t.Run(name, func(t *testing.T) {
			host := "target.example"
			resolver := &staticWebResolver{addresses: map[string][]net.IPAddr{host: addresses}}
			var dialCalls atomic.Int32
			tool, err := NewWebFetch(WebFetchConfig{
				Policy: webFetchPolicy(t, "*"), SubjectID: "web-test", Resolver: resolver,
				DialContext: staticHTTPDial("", &dialCalls), Timeout: time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			url := "http://" + host
			if name == "literal metadata" {
				url = "http://169.254.169.254/latest/meta-data"
			}
			_, err = tool.Execute(context.Background(), WebFetchRequest{URL: url})
			if !errors.Is(err, ErrUnsafeNetworkTarget) {
				t.Fatalf("expected unsafe target error, got %v", err)
			}
			if dialCalls.Load() != 0 {
				t.Fatalf("unsafe target reached dialer %d time(s)", dialCalls.Load())
			}
		})
	}
}

func TestWebFetchBlocksRedirectToLocalNetwork(t *testing.T) {
	resolver := &staticWebResolver{addresses: map[string][]net.IPAddr{
		"public.example": {{IP: net.ParseIP("93.184.216.34")}},
	}}
	var dialCalls atomic.Int32
	response := "HTTP/1.1 302 Found\r\nLocation: http://127.0.0.1/admin\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"
	tool, err := NewWebFetch(WebFetchConfig{
		Policy: webFetchPolicy(t, "*"), SubjectID: "web-test", Resolver: resolver,
		DialContext: staticHTTPDial(response, &dialCalls), Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = tool.Execute(context.Background(), WebFetchRequest{URL: "http://public.example/redirect"})
	if !errors.Is(err, ErrUnsafeNetworkTarget) {
		t.Fatalf("expected redirect to be blocked, got %v", err)
	}
	if dialCalls.Load() != 1 {
		t.Fatalf("redirect target reached dialer; calls=%d", dialCalls.Load())
	}
}

func TestWebFetchRechecksDomainPolicyOnRedirect(t *testing.T) {
	resolver := &staticWebResolver{addresses: map[string][]net.IPAddr{
		"public.example": {{IP: net.ParseIP("93.184.216.34")}},
	}}
	var dialCalls atomic.Int32
	response := "HTTP/1.1 302 Found\r\nLocation: http://other.example/article\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"
	tool, err := NewWebFetch(WebFetchConfig{
		Policy: webFetchPolicy(t, "public.example"), SubjectID: "web-test", Resolver: resolver,
		DialContext: staticHTTPDial(response, &dialCalls), Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = tool.Execute(context.Background(), WebFetchRequest{URL: "http://public.example/redirect"})
	if !errors.Is(err, domain.ErrNotPermitted) {
		t.Fatalf("expected cross-scope redirect denial, got %v", err)
	}
	if dialCalls.Load() != 1 {
		t.Fatalf("out-of-scope redirect reached dialer; calls=%d", dialCalls.Load())
	}
}

func TestWebFetchAppliesPolicyBeforeDNS(t *testing.T) {
	resolver := &staticWebResolver{addresses: map[string][]net.IPAddr{
		"denied.example": {{IP: net.ParseIP("93.184.216.34")}},
	}}
	var dialCalls atomic.Int32
	tool, err := NewWebFetch(WebFetchConfig{
		Policy: webFetchPolicy(t, "allowed.example"), SubjectID: "web-test", Resolver: resolver,
		DialContext: staticHTTPDial("", &dialCalls), Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = tool.Execute(context.Background(), WebFetchRequest{URL: "https://denied.example"})
	if !errors.Is(err, domain.ErrNotPermitted) {
		t.Fatalf("expected policy denial, got %v", err)
	}
	if resolver.calls.Load() != 0 || dialCalls.Load() != 0 {
		t.Fatalf("denied request performed network work: resolve=%d dial=%d", resolver.calls.Load(), dialCalls.Load())
	}
}

func TestWebFetchRejectsBinaryAndTruncatesText(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		wantError   error
		wantContent string
		truncated   bool
	}{
		{name: "binary", contentType: "application/octet-stream", body: "binary", wantError: ErrUnsupportedWebContent},
		{name: "bounded text", contentType: "text/plain; charset=utf-8", body: "abcdefgh", wantContent: "abcd", truncated: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolver := &staticWebResolver{addresses: map[string][]net.IPAddr{"public.example": {{IP: net.ParseIP("93.184.216.34")}}}}
			var dialCalls atomic.Int32
			response := "HTTP/1.1 200 OK\r\nContent-Type: " + test.contentType + "\r\nContent-Length: " + stringInt(len(test.body)) + "\r\nConnection: close\r\n\r\n" + test.body
			tool, err := NewWebFetch(WebFetchConfig{
				Policy: webFetchPolicy(t, "*"), SubjectID: "web-test", Resolver: resolver,
				DialContext: staticHTTPDial(response, &dialCalls), Timeout: time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := tool.Execute(context.Background(), WebFetchRequest{URL: "http://public.example", MaxBytes: 4})
			if test.wantError != nil {
				if !errors.Is(err, test.wantError) {
					t.Fatalf("expected %v, got %v", test.wantError, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.Content != test.wantContent || result.Truncated != test.truncated {
				t.Fatalf("unexpected bounded result %#v", result)
			}
		})
	}
}

func TestRedactedWebFetchArgumentsHidesQueryValues(t *testing.T) {
	redacted := RedactedWebFetchArguments([]byte(`{"url":"https://example.com/path?token=secret&q=hello#fragment","max_bytes":42}`), 4096)
	if strings.Contains(redacted, "secret") || strings.Contains(redacted, "hello") || strings.Contains(redacted, "fragment") {
		t.Fatalf("query or fragment leaked: %s", redacted)
	}
	if !strings.Contains(redacted, `token%3D%255Bredacted%255D`) && !strings.Contains(redacted, `token=%5Bredacted%5D`) {
		t.Fatalf("redacted query key was not preserved: %s", redacted)
	}
}

func stringInt(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [32]byte
	index := len(digits)
	for value > 0 {
		index--
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[index:])
}
