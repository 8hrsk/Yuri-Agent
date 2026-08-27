package domain

import "testing"

func TestCapabilitySetAddIsCopy(t *testing.T) {
	base := CapabilitySet{CapabilityFilesystemRead}
	updated, err := base.Add(CapabilityFilesystemWrite)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Has(CapabilityFilesystemRead) || !updated.Has(CapabilityFilesystemWrite) {
		t.Fatalf("updated set = %#v", updated)
	}
	if base.Has(CapabilityFilesystemWrite) {
		t.Fatalf("base set was mutated: %#v", base)
	}
	if _, err := base.Add(Capability("unknown")); err == nil {
		t.Fatal("unknown capability accepted")
	}
}

func TestCapabilityScopeValidation(t *testing.T) {
	if !UnrestrictedScope().Valid() {
		t.Fatal("unrestricted scope should be valid")
	}
	if (CapabilityScope{Kind: ScopeFilesystem}).Valid() {
		t.Fatal("filesystem scope without roots should be invalid")
	}
	if (CapabilityScope{Kind: ScopeUnrestricted, Values: []string{"/tmp"}}).Valid() {
		t.Fatal("unrestricted scope with values should be invalid")
	}
}
