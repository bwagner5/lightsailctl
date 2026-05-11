package lightsail

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestStatus_PhaseRoundTrip verifies the new Phase / PhaseSince
// fields marshal and unmarshal cleanly, and that they're omitted
// from the JSON when zero so existing watcher reports stay
// byte-compatible.
func TestStatus_PhaseRoundTrip(t *testing.T) {
	t.Run("phase set", func(t *testing.T) {
		ts := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
		st := Status{
			Instance:   "box-a",
			Timestamp:  ts,
			Status:     "idle",
			Phase:      "building",
			PhaseSince: &ts,
		}
		raw, err := json.Marshal(st)
		if err != nil {
			t.Fatal(err)
		}
		s := string(raw)
		if !strings.Contains(s, `"phase":"building"`) {
			t.Errorf("missing phase field: %s", s)
		}
		if !strings.Contains(s, `"phase_since":`) {
			t.Errorf("missing phase_since field: %s", s)
		}

		var back Status
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatal(err)
		}
		if back.Phase != "building" {
			t.Errorf("Phase = %q; want building", back.Phase)
		}
		if back.PhaseSince == nil || !back.PhaseSince.Equal(ts) {
			t.Errorf("PhaseSince = %v; want %v", back.PhaseSince, ts)
		}
	})

	t.Run("phase omitted when zero", func(t *testing.T) {
		st := Status{Instance: "box-a", Timestamp: time.Now(), Status: "idle"}
		raw, err := json.Marshal(st)
		if err != nil {
			t.Fatal(err)
		}
		s := string(raw)
		if strings.Contains(s, "phase") {
			t.Errorf("zero phase should not appear in JSON: %s", s)
		}
	})
}
