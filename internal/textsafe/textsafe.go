package textsafe

import (
	"strings"
	"unicode/utf8"
)

func Sanitize(value string, limit int) string {
	if limit < 0 {
		limit = 0
	}
	var builder strings.Builder
	builder.Grow(min(len(value), limit))
	for len(value) > 0 && builder.Len() < limit {
		r, size := utf8.DecodeRuneInString(value)
		if r == utf8.RuneError && size == 1 {
			r = '�'
		}
		value = value[size:]
		if r == '\n' || r == '\t' || r >= 0x20 && r != 0x7f {
			encoded := string(r)
			if builder.Len()+len(encoded) > limit {
				break
			}
			builder.WriteString(encoded)
		}
	}
	return strings.TrimSpace(builder.String())
}
