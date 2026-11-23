package firewall

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"pocketone.dev/ext/multisite"
)

// An instance from a remote source (the mothership's instance.watch/v1) crosses the wire as JSON, so its typed
// extensions.firewall arrives as a map with the JSON names; the firewall must still read it.
func TestInstanceConfigSurvivesJSON(t *testing.T) {
	sent := multisite.Instance{ID: "chithi", Extensions: map[string]any{"firewall": InstanceConfig{
		RateLimits: &RateLimits{InstanceHourly: 1000000, InstanceConcurrent: 500, Trusted: &RateLimitSet{IPHourly: 7}},
		TrustedIPs: []string{"203.0.113.9"},
	}}}
	b, err := json.Marshal(sent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"instance_hourly":1000000`) || !strings.Contains(string(b), `"trusted_ips"`) {
		t.Fatalf("wire names: %s", b)
	}
	var got multisite.Instance
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if _, typed := got.Extensions["firewall"].(InstanceConfig); typed {
		t.Fatal("expected a decoded map, not the typed value")
	}

	var ic InstanceConfig
	if err := multisite.DecodeExtensionConfig(got.ExtensionConfig("firewall"), &ic); err != nil {
		t.Fatal(err)
	}
	if ic.RateLimits == nil || ic.RateLimits.InstanceHourly != 1000000 || ic.RateLimits.InstanceConcurrent != 500 ||
		ic.RateLimits.Trusted == nil || ic.RateLimits.Trusted.IPHourly != 7 {
		t.Fatalf("rate limits lost: %+v", ic.RateLimits)
	}
	if len(ic.TrustedIPs) != 1 || ic.TrustedIPs[0] != "203.0.113.9" {
		t.Fatalf("trusted ips lost: %v", ic.TrustedIPs)
	}

	fw, _ := NewFirewall(Config{})
	w := httptest.NewRecorder()
	rel, ok := fw.Admit(w, fwRequest("192.0.2.1:1", "/api/health", nil), got)
	if !ok {
		t.Fatalf("refused: %d", w.Code)
	}
	rel()
	if v := w.Header().Get("X-RateLimit-Instance-Hourly-Limit"); v != "1000000" {
		t.Errorf("Instance-Hourly-Limit header = %q, want 1000000", v)
	}
}

// Settings under names the firewall doesn't know (e.g. Go field names from a value encoded without JSON tags) are an
// error, not a silent zero value.
func TestInstanceConfigUnknownKeysFail(t *testing.T) {
	var ic InstanceConfig
	err := multisite.DecodeExtensionConfig(map[string]any{"RateLimits": map[string]any{"InstanceHourly": 1000000}}, &ic)
	if err == nil {
		t.Fatalf("expected an error, decoded %+v", ic)
	}
}
