package grpcclient

import "testing"

// A bare host:port must become dns:/// or round_robin has a single address to
// choose between and the load-balancing fix silently does nothing.
func TestNormalizeTarget(t *testing.T) {
	for in, want := range map[string]string{
		"identityd.ecom.local:50051":        "dns:///identityd.ecom.local:50051",
		"localhost:50051":                   "dns:///localhost:50051",
		"dns:///identityd.ecom.local:50051": "dns:///identityd.ecom.local:50051",
		"passthrough:///127.0.0.1:50051":    "passthrough:///127.0.0.1:50051",
		"unix:///tmp/x.sock":                "unix:///tmp/x.sock",
	} {
		if got := normalizeTarget(in); got != want {
			t.Errorf("normalizeTarget(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDialRequiresRegistry(t *testing.T) {
	if _, err := Dial("localhost:1", Options{}); err == nil {
		t.Fatal("want an error when no registry is supplied")
	}
}
