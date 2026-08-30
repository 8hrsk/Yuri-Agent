package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

const (
	WebFetchToolID         = "web.fetch"
	defaultWebOutputBytes  = int64(256 * 1024)
	hardWebOutputBytes     = int64(2 * 1024 * 1024)
	defaultWebTimeout      = 20 * time.Second
	defaultWebMaxRedirects = 5
	maxWebURLLength        = 4096
)

var (
	ErrUnsafeNetworkTarget   = errors.New("tools: unsafe network target")
	ErrUnsupportedWebContent = errors.New("tools: unsupported web content")
)

type webHostResolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

type WebFetchConfig struct {
	Policy         domain.PolicyEngine
	SubjectID      domain.ID
	MaxOutputBytes int64
	Timeout        time.Duration
	MaxRedirects   int

	// Resolver and DialContext are seams for deterministic tests. Production
	// callers leave both nil and use the system resolver and net.Dialer.
	Resolver    webHostResolver
	DialContext func(ctx context.Context, network, address string) (net.Conn, error)
}

type WebFetchRequest struct {
	URL      string `json:"url"`
	MaxBytes int64  `json:"max_bytes,omitempty"`
}

type WebFetchResult struct {
	URL         string `json:"url"`
	FinalURL    string `json:"final_url"`
	StatusCode  int    `json:"status_code"`
	ContentType string `json:"content_type"`
	Title       string `json:"title,omitempty"`
	Content     string `json:"content"`
	BytesRead   int64  `json:"bytes_read"`
	Truncated   bool   `json:"truncated"`
}

// WebFetchTool performs one bounded, credential-free GET against the public
// internet. Its transport resolves and pins a validated public IP for every
// connection, including redirects, so a DNS rebinding cannot turn a checked
// hostname into access to localhost, a private LAN or cloud metadata.
type WebFetchTool struct {
	policy         domain.PolicyEngine
	subjectID      domain.ID
	maxOutputBytes int64
	client         *http.Client
}

func NewWebFetch(config WebFetchConfig) (*WebFetchTool, error) {
	if config.SubjectID.Empty() {
		return nil, fmt.Errorf("%w: web fetch subject id is required", domain.ErrInvalidArgument)
	}
	if config.Policy == nil {
		return nil, fmt.Errorf("%w: web fetch policy is required", domain.ErrInvalidArgument)
	}
	if config.MaxOutputBytes <= 0 {
		config.MaxOutputBytes = defaultWebOutputBytes
	} else if config.MaxOutputBytes > hardWebOutputBytes {
		config.MaxOutputBytes = hardWebOutputBytes
	}
	if config.Timeout <= 0 {
		config.Timeout = defaultWebTimeout
	}
	if config.MaxRedirects <= 0 {
		config.MaxRedirects = defaultWebMaxRedirects
	}
	resolver := config.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	dial := config.DialContext
	if dial == nil {
		dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
		dial = dialer.DialContext
	}
	transport := &http.Transport{
		Proxy:                  nil,
		ForceAttemptHTTP2:      true,
		MaxIdleConns:           16,
		MaxIdleConnsPerHost:    2,
		MaxConnsPerHost:        2,
		IdleConnTimeout:        30 * time.Second,
		TLSHandshakeTimeout:    10 * time.Second,
		MaxResponseHeaderBytes: 64 * 1024,
	}
	transport.DialContext = publicDialContext(resolver, dial)
	client := &http.Client{Transport: transport, Timeout: config.Timeout}
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= config.MaxRedirects {
			return fmt.Errorf("web fetch: stopped after %d redirects", config.MaxRedirects)
		}
		target, err := parsePublicWebURL(request.URL.String())
		if err != nil {
			return err
		}
		decision, err := config.Policy.Evaluate(domain.PermissionRequest{
			SubjectID: config.SubjectID, Capability: domain.CapabilityNetworkHTTP,
			Scope:  domain.CapabilityScope{Kind: domain.ScopeNetwork, Values: []string{target.Hostname()}},
			Action: "web.fetch redirect " + target.Hostname(), Risk: domain.RiskLow,
		})
		if err != nil {
			return err
		}
		if decision.Decision != domain.PermissionAllow {
			return fmt.Errorf("%w: redirect is outside the granted network scope", domain.ErrNotPermitted)
		}
		return nil
	}
	return &WebFetchTool{
		policy: config.Policy, subjectID: config.SubjectID,
		maxOutputBytes: config.MaxOutputBytes, client: client,
	}, nil
}

func (tool *WebFetchTool) ID() string { return WebFetchToolID }

func (tool *WebFetchTool) Definition() ToolDefinition {
	return ToolDefinition{
		ID: WebFetchToolID,
		Description: "Fetch one public HTTP(S) page without browser cookies or credentials. " +
			"Private, loopback, link-local and metadata endpoints are blocked; output is bounded text.",
		Risk:         domain.RiskLow,
		Capabilities: []domain.Capability{domain.CapabilityNetworkHTTP},
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"url":       map[string]any{"type": "string", "description": "Public absolute HTTP(S) URL."},
				"max_bytes": map[string]any{"type": "integer", "minimum": 1},
			},
			"required": []string{"url"},
		},
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"final_url":    map[string]any{"type": "string"},
				"status_code":  map[string]any{"type": "integer"},
				"content_type": map[string]any{"type": "string"},
				"title":        map[string]any{"type": "string"},
				"content":      map[string]any{"type": "string"},
				"truncated":    map[string]any{"type": "boolean"},
			},
		},
	}
}

func (tool *WebFetchTool) Execute(ctx context.Context, request WebFetchRequest) (WebFetchResult, error) {
	if tool == nil || tool.client == nil || tool.policy == nil {
		return WebFetchResult{}, fmt.Errorf("%w: web fetch tool is not initialized", domain.ErrInvalidArgument)
	}
	if err := contextError(ctx); err != nil {
		return WebFetchResult{}, err
	}
	if request.MaxBytes < 0 {
		return WebFetchResult{}, fmt.Errorf("%w: max_bytes cannot be negative", domain.ErrInvalidArgument)
	}
	target, err := parsePublicWebURL(request.URL)
	if err != nil {
		return WebFetchResult{}, err
	}
	decision, err := tool.policy.Evaluate(domain.PermissionRequest{
		SubjectID:  tool.subjectID,
		Capability: domain.CapabilityNetworkHTTP,
		Scope:      domain.CapabilityScope{Kind: domain.ScopeNetwork, Values: []string{target.Hostname()}},
		Action:     "web.fetch " + target.Hostname(),
		Risk:       domain.RiskLow,
	})
	if err != nil {
		return WebFetchResult{}, err
	}
	if decision.Decision != domain.PermissionAllow {
		return WebFetchResult{}, fmt.Errorf("%w: %s", domain.ErrNotPermitted, decision.Reason)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return WebFetchResult{}, fmt.Errorf("create web request: %w", err)
	}
	httpRequest.Header.Set("Accept", "text/html, text/plain, application/json, application/xml;q=0.9, text/*;q=0.8")
	httpRequest.Header.Set("User-Agent", "Yuri-Agent/0.1 (+https://github.com/OrdoAI/Yuri-Agent)")
	response, err := tool.client.Do(httpRequest)
	if err != nil {
		return WebFetchResult{}, fmt.Errorf("fetch web page: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return WebFetchResult{}, fmt.Errorf("fetch web page: remote server returned %s", response.Status)
	}
	contentType, htmlContent, err := supportedWebContentType(response.Header.Get("Content-Type"))
	if err != nil {
		return WebFetchResult{}, err
	}
	limit := tool.outputLimit(request.MaxBytes)
	raw, truncated, err := readBounded(response.Body, limit)
	if err != nil {
		return WebFetchResult{}, fmt.Errorf("read web page: %w", err)
	}
	if contentType == "" {
		contentType, htmlContent, err = supportedWebContentType(http.DetectContentType(raw))
		if err != nil {
			return WebFetchResult{}, err
		}
	}
	decoded, decodeTruncated, err := decodeWebText(raw, response.Header.Get("Content-Type"), limit)
	if err != nil {
		return WebFetchResult{}, err
	}
	truncated = truncated || decodeTruncated
	content := strings.ToValidUTF8(string(decoded), "�")
	title := ""
	if htmlContent {
		title, content = extractHTMLText(content)
	}
	return WebFetchResult{
		URL: target.String(), FinalURL: response.Request.URL.String(), StatusCode: response.StatusCode,
		ContentType: contentType, Title: title, Content: content,
		BytesRead: int64(len(decoded)), Truncated: truncated,
	}, nil
}

func (tool *WebFetchTool) outputLimit(requested int64) int64 {
	if requested <= 0 || requested > tool.maxOutputBytes {
		return tool.maxOutputBytes
	}
	return requested
}

func parsePublicWebURL(rawURL string) (*url.URL, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" || len(rawURL) > maxWebURLLength || containsASCIIControl(rawURL) {
		return nil, fmt.Errorf("%w: invalid URL", domain.ErrInvalidArgument)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%w: parse URL: %v", domain.ErrInvalidArgument, err)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("%w: only HTTP(S) URLs are supported", domain.ErrInvalidArgument)
	}
	if parsed.Hostname() == "" || parsed.User != nil || strings.Contains(parsed.Hostname(), "%") {
		return nil, fmt.Errorf("%w: URL host is invalid", domain.ErrInvalidArgument)
	}
	port := parsed.Port()
	if port != "" && !((parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443")) {
		return nil, fmt.Errorf("%w: only standard HTTP(S) ports are supported", ErrUnsafeNetworkTarget)
	}
	hostname := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") || strings.HasSuffix(hostname, ".local") || strings.HasSuffix(hostname, ".internal") {
		return nil, fmt.Errorf("%w: local hostnames are blocked", ErrUnsafeNetworkTarget)
	}
	parsed.Host = hostname
	if address, addressErr := netip.ParseAddr(hostname); addressErr == nil && address.Is6() {
		parsed.Host = "[" + hostname + "]"
	}
	if port != "" {
		parsed.Host = net.JoinHostPort(hostname, port)
	}
	parsed.Fragment = ""
	parsed.User = nil
	return parsed, nil
}

func publicDialContext(resolver webHostResolver, dial func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid network address", ErrUnsafeNetworkTarget)
		}
		addresses, err := resolvePublicAddresses(ctx, resolver, host)
		if err != nil {
			return nil, err
		}
		return dial(ctx, network, net.JoinHostPort(addresses[0].String(), port))
	}
}

func resolvePublicAddresses(ctx context.Context, resolver webHostResolver, host string) ([]netip.Addr, error) {
	if parsed, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		parsed = parsed.Unmap()
		if !isPublicInternetAddress(parsed) {
			return nil, fmt.Errorf("%w: non-public IP address", ErrUnsafeNetworkTarget)
		}
		return []netip.Addr{parsed}, nil
	}
	resolved, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve web host: %w", err)
	}
	if len(resolved) == 0 {
		return nil, fmt.Errorf("resolve web host: no addresses")
	}
	addresses := make([]netip.Addr, 0, len(resolved))
	for _, candidate := range resolved {
		address, ok := netip.AddrFromSlice(candidate.IP)
		if !ok || candidate.Zone != "" {
			return nil, fmt.Errorf("%w: invalid resolved address", ErrUnsafeNetworkTarget)
		}
		address = address.Unmap()
		if !isPublicInternetAddress(address) {
			// Reject a mixed public/private answer as a whole rather than picking
			// the convenient public member and leaving a rebinding primitive.
			return nil, fmt.Errorf("%w: host resolves to a non-public address", ErrUnsafeNetworkTarget)
		}
		addresses = append(addresses, address)
	}
	return addresses, nil
}

var blockedNetworkPrefixes = mustNetworkPrefixes(
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
	"172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15",
	"198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
	"::/128", "::1/128", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2002::/16",
	"fc00::/7", "fec0::/10", "fe80::/10", "ff00::/8",
)

func mustNetworkPrefixes(values ...string) []netip.Prefix {
	result := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		result = append(result, netip.MustParsePrefix(value))
	}
	return result
}

func isPublicInternetAddress(address netip.Addr) bool {
	if !address.IsValid() || !address.IsGlobalUnicast() {
		return false
	}
	for _, blocked := range blockedNetworkPrefixes {
		if blocked.Contains(address) {
			return false
		}
	}
	return true
}

func supportedWebContentType(header string) (mediaType string, htmlContent bool, err error) {
	if strings.TrimSpace(header) == "" {
		return "", false, nil
	}
	mediaType, _, parseErr := mime.ParseMediaType(header)
	if parseErr != nil {
		return "", false, fmt.Errorf("%w: invalid Content-Type", ErrUnsupportedWebContent)
	}
	mediaType = strings.ToLower(mediaType)
	switch {
	case mediaType == "text/html", mediaType == "application/xhtml+xml":
		return mediaType, true, nil
	case strings.HasPrefix(mediaType, "text/"), mediaType == "application/json", mediaType == "application/xml", mediaType == "application/javascript",
		strings.HasSuffix(mediaType, "+json"), strings.HasSuffix(mediaType, "+xml"):
		return mediaType, false, nil
	default:
		return "", false, fmt.Errorf("%w: %s", ErrUnsupportedWebContent, mediaType)
	}
}

func readBounded(reader io.Reader, limit int64) ([]byte, bool, error) {
	value, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, false, err
	}
	truncated := int64(len(value)) > limit
	if truncated {
		value = value[:limit]
	}
	return value, truncated, nil
}

func decodeWebText(raw []byte, contentType string, limit int64) ([]byte, bool, error) {
	reader, err := charset.NewReader(bytes.NewReader(raw), contentType)
	if err != nil {
		return nil, false, fmt.Errorf("%w: decode charset: %v", ErrUnsupportedWebContent, err)
	}
	return readBounded(reader, limit)
}

func extractHTMLText(source string) (string, string) {
	document, err := html.Parse(strings.NewReader(source))
	if err != nil {
		return "", source
	}
	title := ""
	var findTitle func(*html.Node)
	findTitle = func(node *html.Node) {
		if title != "" {
			return
		}
		if node.Type == html.ElementNode && node.Data == "title" && node.FirstChild != nil {
			title = strings.Join(strings.Fields(node.FirstChild.Data), " ")
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			findTitle(child)
		}
	}
	findTitle(document)

	var builder strings.Builder
	var walk func(*html.Node, bool)
	walk = func(node *html.Node, skipped bool) {
		if node.Type == html.ElementNode {
			switch node.Data {
			case "head", "script", "style", "noscript", "svg", "canvas", "template":
				skipped = true
			}
			if !skipped && isHTMLBlock(node.Data) {
				builder.WriteByte('\n')
			}
		}
		if !skipped && node.Type == html.TextNode {
			value := strings.Join(strings.Fields(node.Data), " ")
			if value != "" {
				builder.WriteString(value)
				builder.WriteByte(' ')
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child, skipped)
		}
		if !skipped && node.Type == html.ElementNode && isHTMLBlock(node.Data) {
			builder.WriteByte('\n')
		}
	}
	walk(document, false)
	lines := strings.Split(builder.String(), "\n")
	compact := make([]string, 0, len(lines))
	for _, line := range lines {
		line = compactHTMLLine(line)
		if line != "" && (len(compact) == 0 || compact[len(compact)-1] != line) {
			compact = append(compact, line)
		}
	}
	return title, strings.Join(compact, "\n")
}

func compactHTMLLine(line string) string {
	line = strings.Join(strings.Fields(line), " ")
	return strings.NewReplacer(
		" .", ".", " ,", ",", " ;", ";", " :", ":", " !", "!", " ?", "?",
		" )", ")", " ]", "]", " }", "}", "( ", "(", "[ ", "[", "{ ", "{",
	).Replace(line)
}

func isHTMLBlock(name string) bool {
	switch name {
	case "address", "article", "aside", "blockquote", "br", "div", "dl", "fieldset", "footer", "form", "h1", "h2", "h3", "h4", "h5", "h6", "header", "hr", "li", "main", "nav", "ol", "p", "pre", "section", "table", "tr", "ul":
		return true
	default:
		return false
	}
}

func containsASCIIControl(value string) bool {
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return true
		}
	}
	return false
}

func normalizedURLForAudit(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	query := parsed.Query()
	for key, values := range query {
		for index := range values {
			values[index] = "[redacted]"
		}
		query[key] = values
	}
	parsed.RawQuery = query.Encode()
	parsed.Fragment = ""
	parsed.User = nil
	hostname := strings.ToLower(parsed.Hostname())
	if parsed.Port() != "" {
		parsed.Host = net.JoinHostPort(hostname, parsed.Port())
	} else if address, addressErr := netip.ParseAddr(hostname); addressErr == nil && address.Is6() {
		parsed.Host = "[" + hostname + "]"
	} else {
		parsed.Host = hostname
	}
	return parsed.String()
}

// RedactedWebFetchArguments preserves the public target shape for Activity
// while removing URL credentials, fragments and every query value.
func RedactedWebFetchArguments(arguments []byte, maxBytes int) string {
	var request WebFetchRequest
	if json.Unmarshal(arguments, &request) != nil {
		return "{}"
	}
	value := map[string]any{"url": normalizedURLForAudit(request.URL)}
	if request.MaxBytes > 0 {
		value["max_bytes"] = request.MaxBytes
	}
	encoded, err := json.Marshal(value)
	if err != nil || maxBytes <= 0 {
		return "{}"
	}
	if len(encoded) <= maxBytes {
		return string(encoded)
	}
	return `{"url":"[truncated]"}`
}
