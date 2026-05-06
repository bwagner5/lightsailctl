// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// Package redact has helpers for referring to sensitive values in logs
// without revealing them. Use these anywhere the log line needs a
// token, URL with signature, PEM body, or bytes-of-secret hint.
package redact

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// String returns a marker with the length of s, so correlation by size
// is possible while the value itself is never revealed.
func String(s string) string {
	return fmt.Sprintf("<redacted len=%d>", len(s))
}

// Prefix returns the first n bytes of s followed by an ellipsis, for
// correlating different log lines that reference the same token. n is
// clamped to len(s) so this never panics; pass n<=0 to hide everything.
func Prefix(s string, n int) string {
	if n <= 0 {
		return "<redacted>"
	}
	if n > len(s) {
		n = len(s)
	}
	return s[:n] + "…"
}

// URL returns rawURL with its query string stripped. Presigned URLs
// carry the signature in the query, so dropping it de-secretizes the
// URL while keeping the host/path for debugging.
func URL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "<redacted>"
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// Bytes formats a byte count like "4.5 KB" without revealing the body.
// Use this when the only safe thing to log about a payload is its size.
func Bytes(n int) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case n < kb:
		return fmt.Sprintf("%d B", n)
	case n < mb:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(kb))
	case n < gb:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(mb))
	default:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(gb))
	}
}

// sensitiveKeyPattern matches top-level JSON keys whose values are
// sensitive. Same shape as the handler's key-name redactor, repeated
// here so JSON bodies (e.g. policy docs) can be redacted before being
// stringified into a log attribute.
var sensitiveKeyPattern = regexp.MustCompile(
	`(?i)(password|passwd|secret|token|authorization|x-amz-signature|` +
		`private[_-]?key|access[_-]?key(_id)?|client[_-]?secret|` +
		`bearer|oauth|session[_-]?token|api[_-]?key|user[_-]?data)`)

// JSON parses raw as JSON, redacts the values of top-level keys whose
// names match the sensitive-key pattern, and returns the result. If raw
// isn't valid JSON, returns "<redacted non-json>" — the safe default is
// to suppress.
func JSON(raw []byte) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "<redacted non-json>"
	}
	redactNode(v)
	b, err := json.Marshal(v)
	if err != nil {
		return "<redacted>"
	}
	return string(b)
}

// redactNode walks a decoded JSON value and replaces sensitive-key
// values with a placeholder. Recurses into maps and arrays.
func redactNode(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k := range t {
			if sensitiveKeyPattern.MatchString(k) {
				if s, ok := t[k].(string); ok {
					t[k] = "<redacted len=" + itoa(len(s)) + ">"
				} else {
					t[k] = "<redacted>"
				}
				continue
			}
			redactNode(t[k])
		}
	case []any:
		for i := range t {
			redactNode(t[i])
		}
	}
}

// itoa is a tiny int-to-string helper so this package stays
// strconv-free (keeps imports minimal / auditable).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b strings.Builder
	if n < 0 {
		b.WriteByte('-')
		n = -n
	}
	var digits [20]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	b.Write(digits[i:])
	return b.String()
}
