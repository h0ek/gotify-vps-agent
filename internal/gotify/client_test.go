package gotify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestURLValidation(t *testing.T) {
	cases := []struct {
		url   string
		allow bool
		ok    bool
	}{
		{"https://notify.example.com", false, true},
		{"https://notify.example.com/gotify", false, true},
		{"http://127.0.0.1:8080", false, true},
		{"http://localhost:8080", false, true},
		{"http://notify.example.com", false, false},
		{"http://notify.example.com", true, true},
		{"ftp://notify.example.com", true, false},
		{"https://user:pass@notify.example.com", false, false},
		{"https://notify.example.com/?key=value", false, false},
	}
	for _, test := range cases {
		_, err := validateURL(test.url, test.allow)
		if (err == nil) != test.ok {
			t.Fatalf("url=%q allow=%t err=%v", test.url, test.allow, err)
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

	client, err := New(server.URL, token, time.Second, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Send(context.Background(), "title", "message", 10); err != nil {
		t.Fatal(err)
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

	client, err := New(redirector.URL, token, time.Second, false)
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

	client, err := New(server.URL, token, time.Second, false)
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
	f.Add("https://notify.example.com", false)
	f.Add("http://127.0.0.1:8080", false)
	f.Add("https://user@example.com", true)
	f.Fuzz(func(t *testing.T, raw string, allow bool) {
		_, _ = validateURL(raw, allow)
	})
}

func TestRejectsUnsafeTokenAndPath(t *testing.T) {
	for _, token := range []string{"", "token with spaces", "token\nheader"} {
		if _, err := New("https://notify.example.com", token, time.Second, false); err == nil {
			t.Fatalf("expected token %q to be rejected", token)
		}
	}
	for _, rawURL := range []string{"https://notify.example.com/a/../b", "https://notify.example.com//api", "https://notify.example.com/a%2f..%2fb"} {
		if _, err := validateURL(rawURL, false); err == nil {
			t.Fatalf("expected URL %q to be rejected", rawURL)
		}
	}
}

func TestSendRejectsInvalidPriority(t *testing.T) {
	client, err := New("https://notify.example.com", "token", time.Second, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Send(context.Background(), "title", "message", 11); err == nil {
		t.Fatal("expected invalid priority to be rejected")
	}
}
