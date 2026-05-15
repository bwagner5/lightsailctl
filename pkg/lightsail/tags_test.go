// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package lightsail

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
)

func TestCLIVersion_NonEmpty(t *testing.T) {
	// Dev builds set versionString to v0.0.0-dev; release builds inject
	// a real tag via ldflags. Either way the function must return a
	// non-empty string.
	if v := CLIVersion(); v == "" {
		t.Errorf("CLIVersion = empty")
	}
}

func TestDefaultResourceTags_ContainsVersion(t *testing.T) {
	tags := DefaultResourceTags()
	v, ok := tags[VersionTagKey]
	if !ok {
		t.Fatalf("DefaultResourceTags missing %q: %+v", VersionTagKey, tags)
	}
	if v == "" {
		t.Errorf("version tag value empty")
	}
	// Belt-and-suspenders: the key never changes shape.
	if !strings.HasPrefix(VersionTagKey, "lightsailctl:") {
		t.Errorf("unexpected version tag key: %q", VersionTagKey)
	}
}

func TestLightsailTagsFromMap_SortsKeys(t *testing.T) {
	in := map[string]string{
		"zebra":  "z",
		"apple":  "a",
		"middle": "m",
	}
	got := lightsailTagsFromMap(in)
	if len(got) != 3 {
		t.Fatalf("len = %d; want 3", len(got))
	}
	wantKeys := []string{"apple", "middle", "zebra"}
	for i, w := range wantKeys {
		if k := aws.ToString(got[i].Key); k != w {
			t.Errorf("[%d] Key = %q; want %q", i, k, w)
		}
	}
}

func TestLightsailTagsFromMap_NilInput(t *testing.T) {
	if got := lightsailTagsFromMap(nil); got != nil {
		t.Errorf("nil input should return nil, got %+v", got)
	}
	if got := lightsailTagsFromMap(map[string]string{}); got != nil {
		t.Errorf("empty input should return nil, got %+v", got)
	}
}

func TestMergeTagMaps(t *testing.T) {
	a := map[string]string{"x": "1", "y": "2"}
	b := map[string]string{"y": "3", "z": "4"}
	got := mergeTagMaps(a, b)
	if got["x"] != "1" || got["y"] != "3" || got["z"] != "4" {
		t.Errorf("merged = %+v", got)
	}
	// Inputs must not be mutated.
	if a["y"] != "2" {
		t.Errorf("source a was mutated: %+v", a)
	}
	if b["z"] != "4" {
		t.Errorf("source b was mutated: %+v", b)
	}
}

func TestMergeTagMaps_NilInputs(t *testing.T) {
	got := mergeTagMaps(nil, nil)
	if got == nil {
		t.Errorf("mergeTagMaps(nil, nil) should return non-nil empty map")
	}
	if len(got) != 0 {
		t.Errorf("want empty, got %+v", got)
	}
}
