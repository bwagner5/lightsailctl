package testkit

import "testing"

func TestInstanceState(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		want   string
		wantOK bool
	}{
		{
			name:   "capitalized field and value",
			raw:    `{"Name":"foo","State":"Running"}`,
			want:   "running",
			wantOK: true,
		},
		{
			name:   "lowercase field and value",
			raw:    `{"name":"foo","state":"running"}`,
			want:   "running",
			wantOK: true,
		},
		{
			name:   "mixed case field",
			raw:    `{"StAtE":"Pending"}`,
			want:   "pending",
			wantOK: true,
		},
		{
			name:   "whitespace around value",
			raw:    `{"state":"  running  "}`,
			want:   "running",
			wantOK: true,
		},
		{
			name:   "pretty-printed with spaces after colons",
			raw:    `{"Name": "foo", "State": "running"}`,
			want:   "running",
			wantOK: true,
		},
		{
			name:   "array with one object (CLI default)",
			raw:    `[{"Name":"foo","State":"running"}]`,
			want:   "running",
			wantOK: true,
		},
		{
			name:   "array pretty-printed",
			raw:    "[\n  {\n    \"Name\": \"foo\",\n    \"State\": \"pending\"\n  }\n]",
			want:   "pending",
			wantOK: true,
		},
		{
			name:   "empty array",
			raw:    `[]`,
			want:   "",
			wantOK: false,
		},
		{
			name:   "no state field",
			raw:    `{"Name":"foo"}`,
			want:   "",
			wantOK: false,
		},
		{
			name:   "invalid json",
			raw:    `not json`,
			want:   "",
			wantOK: false,
		},
		{
			name:   "state not a string",
			raw:    `{"state":123}`,
			want:   "",
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := instanceState(tc.raw)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("instanceState(%q) = (%q, %v), want (%q, %v)",
					tc.raw, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestInstanceGone(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"empty string", "", true},
		{"null", "null", true},
		{"empty object", "{}", true},
		{"empty array", "[]", true},
		{"whitespace around empty array", "  []  \n", true},
		{"non-empty object", `{"Name":"foo"}`, false},
		{"non-empty array", `[{"Name":"foo"}]`, false},
		{"invalid json", `not json`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := instanceGone(tc.raw); got != tc.want {
				t.Errorf("instanceGone(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}
