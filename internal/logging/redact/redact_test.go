// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package redact

import (
	"strings"
	"testing"
)

func TestString(t *testing.T) {
	got := String("abcd")
	if got != "<redacted len=4>" {
		t.Errorf("String(abcd) = %q; want <redacted len=4>", got)
	}
}

func TestPrefix(t *testing.T) {
	if got := Prefix("abcdef", 3); got != "abc…" {
		t.Errorf("Prefix(abcdef, 3) = %q; want abc…", got)
	}
	if got := Prefix("ab", 10); got != "ab…" {
		t.Errorf("Prefix(ab, 10) = %q; want ab…", got)
	}
	if got := Prefix("abc", 0); got != "<redacted>" {
		t.Errorf("Prefix(..., 0) = %q; want <redacted>", got)
	}
}

func TestURLStripsQuery(t *testing.T) {
	got := URL("https://bucket.s3.amazonaws.com/k?X-Amz-Signature=abcd&X-Amz-Expires=3600")
	if got != "https://bucket.s3.amazonaws.com/k" {
		t.Errorf("URL(...) = %q; want bare URL without query", got)
	}
}

func TestURLBadInput(t *testing.T) {
	got := URL("not a url")
	// url.Parse is permissive — it returns "" for a lot of things. Just
	// ensure we don't panic and return SOMETHING.
	if got == "" {
		t.Errorf("URL returned empty string for bad input")
	}
}

func TestBytes(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{512, "512 B"},
		{2048, "2.0 KB"},
		{5 * 1024 * 1024, "5.0 MB"},
	}
	for _, c := range cases {
		if got := Bytes(c.in); got != c.want {
			t.Errorf("Bytes(%d) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestJSONRedactsSensitiveKeys(t *testing.T) {
	raw := []byte(`{"name":"alice","secret":"topkek","nested":{"password":"p"}}`)
	got := JSON(raw)
	if strings.Contains(got, "topkek") {
		t.Errorf("JSON() leaked secret value: %q", got)
	}
	if !strings.Contains(got, "alice") {
		t.Errorf("JSON() dropped non-sensitive value: %q", got)
	}
	// Nested sensitive key should also be redacted (JSON recurses).
	if strings.Contains(got, `"password":"p"`) {
		t.Errorf("JSON() did not recurse into nested sensitive key: %q", got)
	}
}

func TestJSONNonJSON(t *testing.T) {
	got := JSON([]byte("not json"))
	if got != "<redacted non-json>" {
		t.Errorf("JSON(bad) = %q; want <redacted non-json>", got)
	}
}
