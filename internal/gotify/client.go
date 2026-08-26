package gotify

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	pathpkg "path"
	"strings"
	"time"

	"github.com/h0ek/gotify-vps-agent/internal/socksproxy"
	"github.com/h0ek/gotify-vps-agent/internal/textsafe"
)

const maxResponseBody = 8192

type Client struct {
	baseURL *url.URL
	token   string
	http    *http.Client
}

type payload struct {
	Title    string         `json:"title"`
	Message  string         `json:"message"`
	Priority int            `json:"priority"`
	Extras   map[string]any `json:"extras,omitempty"`
}

func New(rawURL, token string, timeout time.Duration, allowInsecureHTTP bool, rawProxyURL string) (*Client, error) {
	proxyURL, err := socksproxy.Parse(rawProxyURL)
	if err != nil {
		return nil, err
	}
	base, err := validateURL(rawURL, allowInsecureHTTP, proxyURL != nil)
	if err != nil {
		return nil, err
	}
	token = strings.TrimSpace(token)
	if err := validateToken(token); err != nil {
		return nil, err
	}
	if timeout < time.Second || timeout > time.Minute {
		return nil, fmt.Errorf("Gotify timeout must be between 1s and 1m")
	}
	transport := &http.Transport{
		DialContext:            (&net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:      true,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:    timeout,
		ResponseHeaderTimeout:  timeout,
		ExpectContinueTimeout:  time.Second,
		MaxIdleConns:           4,
		MaxIdleConnsPerHost:    2,
		IdleConnTimeout:        30 * time.Second,
		MaxResponseHeaderBytes: 32 * 1024,
		DisableCompression:     false,
	}
	if proxyURL != nil {
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	return &Client{
		baseURL: base,
		token:   token,
		http: &http.Client{
			Transport: transport,
			Timeout:   timeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func validateURL(rawURL string, allowInsecureHTTP, proxyEnabled bool) (*url.URL, error) {
	base, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, fmt.Errorf("parse Gotify URL: %w", err)
	}
	if base.Scheme != "https" && base.Scheme != "http" {
		return nil, fmt.Errorf("Gotify URL must use http or https")
	}
	if base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.Opaque != "" {
		return nil, fmt.Errorf("Gotify URL contains unsupported components")
	}
	if strings.ContainsAny(base.Host, "\r\n\t") {
		return nil, fmt.Errorf("Gotify URL contains invalid host characters")
	}
	onion, err := isOnionHost(base.Hostname())
	if err != nil {
		return nil, err
	}
	if onion && !proxyEnabled {
		return nil, fmt.Errorf("Gotify .onion URL requires a SOCKS5 proxy")
	}
	if base.Scheme == "http" && !allowInsecureHTTP && !isLoopbackHost(base.Hostname()) && !(onion && proxyEnabled) {
		return nil, fmt.Errorf("plain HTTP is allowed only for loopback or proxied .onion addresses unless explicitly enabled")
	}
	base.Path = strings.TrimRight(base.Path, "/")
	if base.Path != "" {
		if strings.Contains(base.Path, "\\") || pathpkg.Clean(base.Path) != base.Path {
			return nil, fmt.Errorf("Gotify URL path is not canonical")
		}
	}
	base.RawPath = ""
	return base, nil
}

func isOnionHost(host string) (bool, error) {
	lower := strings.ToLower(host)
	if strings.HasSuffix(lower, ".onion.") {
		return false, fmt.Errorf("Gotify onion host must not use a trailing dot")
	}
	if !strings.HasSuffix(lower, ".onion") {
		return false, nil
	}
	labels := strings.Split(lower, ".")
	if len(labels) < 2 || labels[len(labels)-1] != "onion" {
		return false, fmt.Errorf("Gotify onion host is invalid")
	}
	serviceID := labels[len(labels)-2]
	if len(serviceID) != 56 {
		return false, fmt.Errorf("Gotify onion host must use a v3 service address")
	}
	for _, character := range serviceID {
		if (character < 'a' || character > 'z') && (character < '2' || character > '7') {
			return false, fmt.Errorf("Gotify onion host must use a valid v3 service address")
		}
	}
	return true, nil
}

func validateToken(token string) error {
	if token == "" {
		return fmt.Errorf("application token is empty")
	}
	if len(token) > 4096 {
		return fmt.Errorf("application token is unexpectedly long")
	}
	for _, character := range token {
		if character <= 0x20 || character == 0x7f {
			return fmt.Errorf("application token contains whitespace or control characters")
		}
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (c *Client) endpoint(path string) string {
	copyURL := *c.baseURL
	copyURL.Path = strings.TrimRight(copyURL.Path, "/") + path
	copyURL.RawPath = ""
	return copyURL.String()
}

func (c *Client) Send(ctx context.Context, title, message string, priority int) error {
	if priority < 0 || priority > 10 {
		return fmt.Errorf("Gotify priority must be between 0 and 10")
	}
	body := payload{
		Title:    textsafe.Sanitize(title, 256),
		Message:  textsafe.Sanitize(message, 64*1024),
		Priority: priority,
		Extras: map[string]any{
			"client::display": map[string]string{"contentType": "text/markdown"},
		},
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("/message"), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Gotify-Key", c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("send Gotify message: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Gotify returned HTTP %d: %s", resp.StatusCode, c.redact(readBody(resp.Body)))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBody))
	return nil
}

func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint("/health"), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("contact Gotify health endpoint: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Gotify health endpoint returned HTTP %d", resp.StatusCode)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBody))
	return nil
}

func (c *Client) redact(value string) string {
	if c.token == "" {
		return value
	}
	return strings.ReplaceAll(value, c.token, "[redacted]")
}

func readBody(reader io.Reader) string {
	data, err := io.ReadAll(io.LimitReader(reader, maxResponseBody+1))
	if err != nil && !errors.Is(err, io.EOF) {
		return "unreadable response"
	}
	text := textsafe.Sanitize(string(data), maxResponseBody)
	if text == "" {
		return "empty response"
	}
	return text
}
