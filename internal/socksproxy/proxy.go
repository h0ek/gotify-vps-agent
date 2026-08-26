package socksproxy

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

const DefaultURL = "socks5h://127.0.0.1:9050"

func Parse(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	proxyURL, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse SOCKS5 proxy URL: %w", err)
	}
	if proxyURL.Scheme != "socks5" && proxyURL.Scheme != "socks5h" {
		return nil, fmt.Errorf("SOCKS5 proxy URL must use socks5 or socks5h")
	}
	if proxyURL.Host == "" || proxyURL.User != nil || proxyURL.Path != "" || proxyURL.RawPath != "" || proxyURL.RawQuery != "" || proxyURL.Fragment != "" || proxyURL.Opaque != "" {
		return nil, fmt.Errorf("SOCKS5 proxy URL contains unsupported components")
	}
	if strings.ContainsAny(proxyURL.Host, "\r\n\t") {
		return nil, fmt.Errorf("SOCKS5 proxy URL contains invalid host characters")
	}
	host := proxyURL.Hostname()
	if !isLoopbackHost(host) {
		return nil, fmt.Errorf("SOCKS5 proxy must use a loopback address")
	}
	port := proxyURL.Port()
	if port == "" {
		return nil, fmt.Errorf("SOCKS5 proxy URL must include a port")
	}
	parsedPort, err := strconv.Atoi(port)
	if err != nil || parsedPort < 1 || parsedPort > 65535 {
		return nil, fmt.Errorf("SOCKS5 proxy port must be between 1 and 65535")
	}
	return proxyURL, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
