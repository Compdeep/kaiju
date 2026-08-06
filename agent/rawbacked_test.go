package agent

import "testing"

// Every JSON-backed body answered Field and Evidence identically — five copies
// of the same two lines. RawBacked is that answer once, so a concrete body
// writes only Summary, which is the part that genuinely differs.

type probeBody struct {
	RawBacked
	Out struct{ Name string }
}

func (b probeBody) Summary() string { return "probe: " + b.Out.Name }

func TestRawBackedSuppliesFieldAndEvidence(t *testing.T) {
	const raw = `{"root_cause":"disk full","hosts":["web-1","web-2"]}`
	var b NodeBody = probeBody{RawBacked: RawBacked{Raw: raw}}

	if got := b.Evidence(); got != raw {
		t.Errorf("Evidence() = %q, want the raw JSON", got)
	}
	if v, ok := b.Field("root_cause"); !ok || v != "disk full" {
		t.Errorf(`Field("root_cause") = %v, %v`, v, ok)
	}
	if v, ok := b.Field("hosts.1"); !ok || v != "web-2" {
		t.Errorf(`Field("hosts.1") = %v, %v`, v, ok)
	}
	if _, ok := b.Field("nope"); ok {
		t.Error("a missing path should miss")
	}
	if got := b.Summary(); got != "probe: " {
		t.Errorf("Summary() = %q; the concrete body still owns it", got)
	}
}

// A body that needs different behaviour overrides the method it needs, and the
// override wins. ReflectionBody does exactly this for Evidence.
func TestRawBackedCanBeOverridden(t *testing.T) {
	b := ReflectionBody{Out: reflectionOutput{Summary: "malicious"}}
	if got := b.Evidence(); got != "malicious" {
		t.Errorf("Evidence() = %q; the override should fall back to the summary when there is no raw JSON", got)
	}
}

// The bodies that embed it must still satisfy NodeBody — the compiler is the
// check, so this is a compile-time assertion with a name.
func TestEmbeddingBodiesStillSatisfyNodeBody(t *testing.T) {
	var _ NodeBody = ComputeBody{}
	var _ NodeBody = HolmesBody{}
	var _ NodeBody = MicroPlannerBody{}
	var _ NodeBody = ObserverBody{}
	var _ NodeBody = ReflectionBody{}
	var _ NodeBody = RawTextBody{}
}
