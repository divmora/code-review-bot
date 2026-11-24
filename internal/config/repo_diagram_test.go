package config

import (
	"strings"
	"testing"
)

func TestRepoConfigDiagram(t *testing.T) {
	var d RepoConfig
	d.fillDefaults()
	rc, err := ParseRepoConfig(nil, d)
	if err != nil || rc.Diagram != "off" {
		t.Fatalf("default: %q %v", rc.Diagram, err)
	}
	if rc, err = ParseRepoConfig([]byte("diagram: Auto\n"), rc); err != nil || rc.Diagram != "auto" {
		t.Errorf("auto: %q %v", rc.Diagram, err)
	}
	if _, err := ParseRepoConfig([]byte("diagram: sometimes\n"), rc); err == nil || !strings.Contains(err.Error(), "want off, auto or always") {
		t.Errorf("invalid: %v", err)
	}
	always, bad := "always", "maybe"
	ov := &Overrides{Diagram: &always}
	if err := ov.Validate(nil); err != nil {
		t.Fatal(err)
	}
	ov.Apply(&rc)
	if rc.Diagram != "always" {
		t.Errorf("override: %q", rc.Diagram)
	}
	if err := (&Overrides{Diagram: &bad}).Validate(nil); err == nil {
		t.Error("an invalid override is refused")
	}
}
