package dispatch

import "testing"

func TestToolchainReadsTheConvertedForm(t *testing.T) {
	bps := readBlueprints(t, []string{"architecture", "protocol", "patterns", "agents", "platforms", "schemas"})
	types := TypeFields(bps)
	t.Logf("types indexed: %d", len(types))
	for _, c := range []struct {
		typ, field string
		want       bool
	}{
		{"ErrorResponse", "error", true},
		{"ErrorResponse", "message", false},
		{"HealthStatus", "uptime", true},
		{"HealthStatus", "checks", true},
		{"HealthStatus", "uptime_seconds", false},
		{"ServiceDirectory", "services", true},
		{"ServiceDirectory", "agents", false},
		{"AgentManifest", "public_key", true},
		{"ChannelGrant", "channel_token", true},
		{"TaskRequest", "id", true},
		{"TaskRequest", "target_agent", false},
	} {
		if got := types[c.typ][c.field]; got != c.want {
			t.Errorf("%s.%s = %v, want %v", c.typ, c.field, got, c.want)
		}
	}
	t.Logf("field-binding faults: %d", len(CheckFieldBindings(bps, nil)))
}
