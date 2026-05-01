package app

import (
	"testing"

	"github.com/aws/lightsailctl/pkg/lightsail"
)

func TestAggregate(t *testing.T) {
	buckets := []lightsail.Bucket{
		{Name: "ls--123--foo--dev", State: "OK", Region: "us-east-1"},
		{Name: "ls--123--foo--prod", State: "OK", Region: "us-east-1"},
		{Name: "ls--123--bar--dev", State: "OK", Region: "us-east-1"},
		{Name: "other--junk", State: "OK", Region: "us-east-1"}, // ignored
	}
	got := aggregate(buckets)
	if len(got) != 2 {
		t.Fatalf("want 2 apps, got %d: %+v", len(got), got)
	}
	bar, foo := got[0].(App), got[1].(App)
	if bar.Name != "bar" || bar.Envs != "dev" {
		t.Errorf("bar: %+v", bar)
	}
	if foo.Name != "foo" || foo.Envs != "dev,prod" {
		t.Errorf("foo: %+v", foo)
	}
}
