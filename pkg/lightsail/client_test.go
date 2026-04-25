package lightsail

import "testing"

func TestParseAppEnv(t *testing.T) {
	cases := []struct {
		in      string
		wantApp string
		wantEnv string
	}{
		{"ls--123--foo--dev", "foo", "dev"},
		{"ls--123--foo--prod", "foo", "prod"},
		{"ls--123--foo", "", ""},         // app-config bucket, no env
		{"other--123--foo--dev", "", ""}, // wrong prefix
		{"ls--123--foo--", "", ""},       // empty env
		{"", "", ""},
	}
	for _, c := range cases {
		gotApp, gotEnv := ParseAppEnv(c.in)
		if gotApp != c.wantApp || gotEnv != c.wantEnv {
			t.Errorf("ParseAppEnv(%q) = (%q, %q); want (%q, %q)", c.in, gotApp, gotEnv, c.wantApp, c.wantEnv)
		}
	}
}

func TestParseAppFromAppBucket(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"ls--123--foo", "foo"},
		{"ls--123--foo--dev", ""},
		{"other--123--foo", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := ParseAppFromAppBucket(c.in); got != c.want {
			t.Errorf("ParseAppFromAppBucket(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestBucketNames(t *testing.T) {
	if got := AppBucketName("123", "foo"); got != "ls--123--foo" {
		t.Errorf("AppBucketName: %q", got)
	}
	if got := EnvBucketName("123", "foo", "dev"); got != "ls--123--foo--dev" {
		t.Errorf("EnvBucketName: %q", got)
	}
}

func TestPrioritizeRegions(t *testing.T) {
	all := []string{"ap-south-1", "eu-west-1", "us-east-1", "us-east-2"}
	cases := []struct {
		name  string
		hints []string
		want  []string
	}{
		{"no hints", nil, []string{"ap-south-1", "eu-west-1", "us-east-1", "us-east-2"}},
		{"single hint", []string{"us-east-2"}, []string{"us-east-2", "ap-south-1", "eu-west-1", "us-east-1"}},
		{"two hints preserve order", []string{"eu-west-1", "us-east-1"},
			[]string{"eu-west-1", "us-east-1", "ap-south-1", "us-east-2"}},
		{"unknown hint dropped", []string{"fake-1", "us-east-2"}, []string{"us-east-2", "ap-south-1", "eu-west-1", "us-east-1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := prioritizeRegions(all, c.hints)
			if len(got) != len(c.want) {
				t.Fatalf("len=%d want=%d: %v", len(got), len(c.want), got)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("got %v; want %v", got, c.want)
				}
			}
		})
	}
}
