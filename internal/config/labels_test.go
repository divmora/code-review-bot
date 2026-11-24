package config

import (
	"strings"
	"testing"
)

func TestRepoConfigLabels(t *testing.T) {
	var d RepoConfig
	d.fillDefaults()
	rc, err := ParseRepoConfig(nil, d)
	if err != nil || rc.Labels.Mode != LabelsOff || rc.Labels.Max != 4 {
		t.Fatalf("defaults: %+v %v", rc.Labels, err)
	}
	yaml := "labels:\n  mode: Suggest\n  allow: [\"type::*\"]\n  suggest_only: [\"priority::*\"]\n  max: 3\n  instructions: be careful\n"
	if rc, err = ParseRepoConfig([]byte(yaml), d); err != nil || rc.Labels.Mode != LabelsSuggest || rc.Labels.Max != 3 || len(rc.Labels.Allow) != 1 || rc.Labels.Instructions != "be careful" {
		t.Fatalf("parsed: %+v %v", rc.Labels, err)
	}
	for _, bad := range []string{"labels:\n  mode: always\n", "labels:\n  max: 50\n"} {
		if _, err := ParseRepoConfig([]byte(bad), d); err == nil || !strings.Contains(err.Error(), "labels") {
			t.Errorf("%q: %v", bad, err)
		}
	}
	ov := &Overrides{Labels: &Labels{Mode: LabelsApply, Allow: []string{"bug::*"}}}
	if err := ov.Validate(nil); err != nil {
		t.Fatal(err)
	}
	ov.Apply(&rc)
	if rc.Labels.Mode != LabelsApply || strings.Join(rc.Labels.Allow, ",") != "bug::*" || rc.Labels.SuggestOnly[0] != "priority::*" || rc.Labels.Max != 3 {
		t.Errorf("override keeps unset fields: %+v", rc.Labels)
	}
	if err := (&Overrides{Labels: &Labels{Mode: "maybe"}}).Validate(nil); err == nil {
		t.Error("invalid override mode")
	}
}
