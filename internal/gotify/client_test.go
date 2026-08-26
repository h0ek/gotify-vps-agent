package gotify

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testOnionHost = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.onion"

func TestURLValidation(t *testing.T) {
	cases := []struct {
		url     string
		allow   bool
		proxy   bool
		allowed bool
	}{
		{"https://notify.example.com", false, false, true},
		{"https://notify.example.com/gotify", false, false, true},
		{"http://127.0.0.1:8080", false, false, true},
		{"http://localhost:8080", false, false, true},
		{"http://notify.example.com", false, false, false},
		{"http://notify.example.com", true, false, true},
		{"http://" + testOnionHost, false, true, true},
		{"https://" + testOnionHost, false, true, true},
		{"http://" + testOnionHost, false, false, false},
		{"https://" + testOnionHost, false, false, false},
		{"http://invalid.onion", false, true, false},
		{"https://" + testOnionHost + ".", false, false, false},
		{"ftp://notify.example.com", true, false, false},
		{"https://user:pass@notify.example.com", false, false, false},
		{"https://notify.example.com/?key=value", false, false, false},
	}
	for _, test := range cases {
		_, err := validateURL(test.url, test.allow, test.proxy)
		if (err == nil) != test.allowed {
			t.Fatalf("url=%q allow=%t proxy=%t err=%v", test.url, test.allow, test.proxy, err)
		}
	}
}

func TestSendUsesApplicationTokenHeader(t *testing.T) {
	const token = "secret-application-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/message" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("X-Gotify-Key") != token {
			t.Fatalf("missing application token header")
		}
		if r.Header.Get("Authorization") != "" {
			t.Fatalf("unexpected authorization header")
		}
		var body payload
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Title != "title" || body.Message != "message" || body.Priority != 10 {
			t.Fatalf("unexpected payload: %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer server.Close()

	client, err := New(server.URL, token, time.Second, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Send(context.Background(), "title", "message", 10); err != nil {
		t.Fatal(err)
	}
}

func TestSendOnionThroughSOCKS5(t *testing.T) {
	const token = "secret-application-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != testOnionHost {
			t.Fatalf("unexpected Host header: %s", r.Host)
		}
		if r.Header.Get("X-Gotify-Key") != token {
			t.Fatalf("missing application token header")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer server.Close()

	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL, requested, stop := startSOCKS5(t, target.Host)
	defer stop()

	client, err := New("http://"+testOnionHost, token, 2*time.Second, false, proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Send(context.Background(), "title", "message", 5); err != nil {
		t.Fatal(err)
	}
	select {
	case address := <-requested:
		if address != testOnionHost+":80" {
			t.Fatalf("unexpected SOCKS5 target: %s", address)
		}
	case <-time.After(time.Second):
		t.Fatal("SOCKS5 target was not recorded")
	}
}

func TestSendDoesNotFollowRedirectsOrLeakToken(t *testing.T) {
	const token = "secret-application-token"
	leaked := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("X-Gotify-Key") != ""
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	client, err := New(redirector.URL, token, time.Second, false, "")
	if err != nil {
		t.Fatal(err)
	}
	err = client.Send(context.Background(), "title", "message", 5)
	if err == nil || !strings.Contains(err.Error(), "HTTP 307") {
		t.Fatalf("expected redirect rejection, got %v", err)
	}
	if leaked {
		t.Fatal("application token leaked to redirect target")
	}
}

func TestSendRedactsReflectedTokenAndControls(t *testing.T) {
	const token = "secret-application-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("invalid\x1b token " + token))
	}))
	defer server.Close()

	client, err := New(server.URL, token, time.Second, false, "")
	if err != nil {
		t.Fatal(err)
	}
	err = client.Send(context.Background(), "title", "message", 5)
	if err == nil {
		t.Fatal("expected an HTTP error")
	}
	if strings.Contains(err.Error(), token) || !strings.Contains(err.Error(), "[redacted]") || strings.ContainsRune(err.Error(), '\x1b') {
		t.Fatalf("unsafe error: %v", err)
	}
}

func FuzzValidateURL(f *testing.F) {
	f.Add("https://notify.example.com", false, false)
	f.Add("http://127.0.0.1:8080", false, false)
	f.Add("http://"+testOnionHost, false, true)
	f.Fuzz(func(t *testing.T, raw string, allow, proxy bool) {
		_, _ = validateURL(raw, allow, proxy)
	})
}

func TestRejectsUnsafeTokenAndPath(t *testing.T) {
	for _, token := range []string{"", "token with spaces", "token\nheader"} {
		if _, err := New("https://notify.example.com", token, time.Second, false, ""); err == nil {
			t.Fatalf("expected token %q to be rejected", token)
		}
	}
	for _, rawURL := range []string{"https://notify.example.com/a/../b", "https://notify.example.com//api", "https://notify.example.com/a%2f..%2fb"} {
		if _, err := validateURL(rawURL, false, false); err == nil {
			t.Fatalf("expected URL %q to be rejected", rawURL)
		}
	}
}

func TestSendRejectsInvalidPriority(t *testing.T) {
	client, err := New("https://notify.example.com", "token", time.Second, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Send(context.Background(), "title", "message", 11); err == nil {
		t.Fatal("expected invalid priority to be rejected")
	}
}

func startSOCKS5(t *testing.T, target string) (string, <-chan string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	requested := make(chan string, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		if err := handleSOCKS5(connection, target, requested); err != nil {
			return
		}
	}()
	return "socks5h://" + listener.Addr().String(), requested, func() { _ = listener.Close() }
}

func handleSOCKS5(connection net.Conn, target string, requested chan<- string) error {
	header := make([]byte, 2)
	if _, err := io.ReadFull(connection, header); err != nil {
		return err
	}
	if header[0] != 5 || header[1] == 0 {
		return fmt.Errorf("invalid SOCKS5 greeting")
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(connection, methods); err != nil {
		return err
	}
	if _, err := connection.Write([]byte{5, 0}); err != nil {
		return err
	}
	request := make([]byte, 4)
	if _, err := io.ReadFull(connection, request); err != nil {
		return err
	}
	if request[0] != 5 || request[1] != 1 {
		return fmt.Errorf("invalid SOCKS5 request")
	}
	host, err := readSOCKS5Host(connection, request[3])
	if err != nil {
		return err
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(connection, portBytes); err != nil {
		return err
	}
	port := binary.BigEndian.Uint16(portBytes)
	requested <- net.JoinHostPort(host, strconv.Itoa(int(port)))

	upstream, err := net.Dial("tcp", target)
	if err != nil {
		return err
	}
	defer upstream.Close()
	if _, err := connection.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
		return err
	}
	done := make(chan struct{}, 1)
	go func() {
		_, _ = io.Copy(upstream, connection)
		done <- struct{}{}
	}()
	_, _ = io.Copy(connection, upstream)
	<-done
	return nil
}

func readSOCKS5Host(connection net.Conn, addressType byte) (string, error) {
	switch addressType {
	case 1:
		value := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(connection, value); err != nil {
			return "", err
		}
		return net.IP(value).String(), nil
	case 3:
		length := make([]byte, 1)
		if _, err := io.ReadFull(connection, length); err != nil {
			return "", err
		}
		value := make([]byte, int(length[0]))
		if _, err := io.ReadFull(connection, value); err != nil {
			return "", err
		}
		return string(value), nil
	case 4:
		value := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(connection, value); err != nil {
			return "", err
		}
		return net.IP(value).String(), nil
	default:
		return "", fmt.Errorf("unsupported SOCKS5 address type")
	}
}
