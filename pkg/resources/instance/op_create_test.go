// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package instance

import (
	"testing"

	"github.com/bwagner5/triad/pkg/registry"
)

func TestCreateFieldsMonitoringIsTypedBool(t *testing.T) {
	fields := CreateFields(NewStore(nil, nil))
	for _, f := range fields {
		if f.Flag != "monitoring" {
			continue
		}
		if f.Kind != registry.KindBool {
			t.Fatalf("monitoring kind = %v; want %v", f.Kind, registry.KindBool)
		}
		return
	}
	t.Fatal("monitoring field not found")
}
