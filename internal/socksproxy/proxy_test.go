package socksproxy

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		value string
		ok    bool
	}{
		{"", true},
		{"socks5h://127.0.0.1:9050", true},
		{"socks5://localhost:9050", true},
		{"socks5h://[::1]:9050", true},
		{"http://127.0.0.1:9050", false},
		{"socks5h://192.0.2.1:9050", false},
		{"socks5h://user:pass@127.0.0.1:9050", false},
		{"socks5h://127.0.0.1", false},
		{"socks5h://127.0.0.1:0", false},
		{"socks5h://127.0.0.1:9050/path", false},
		{"socks5h://127.0.0.1:9050?x=1", false},
	}
	for _, test := range cases {
		_, err := Parse(test.value)
		if (err == nil) != test.ok {
			t.Fatalf("value=%q err=%v", test.value, err)
		}
	}
}
