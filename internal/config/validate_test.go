package config

import (
	"strings"
	"testing"
)

func TestStrict(t *testing.T) {
	t.Setenv("CODEREVIEW_MODEL", "")
	ok := "model: fast\nfallback: [openai:gpt-x]\nmodels:\n  fast: {provider: bedrock, model: m, region: us-east-1}\nreview: {verify_model: fast, verify_can_drop: all}\n"
	cfg, err := Strict("inline", []byte(ok))
	if err != nil {
		t.Fatalf("valid config: %v", err)
	}
	if cfg.Review.VerifyModel != "fast" {
		t.Errorf("config not decoded: %+v", cfg.Review)
	}

	for name, tc := range map[string]struct{ yaml, want string }{
		"unknown key":       {"model: openai:x\nreview: {verify_modle: y}\n", "verify_modle"},
		"unknown model key": {"model: a\nmodels:\n  a: {provider: openai, model: x, regoin: y}\n", "regoin"},
		"unknown provider":  {"model: a\nmodels:\n  a: {provider: bedrok, model: x}\n", `unknown provider "bedrok"`},
		"incomplete":        {"model: a\nmodels:\n  a: {provider: openai}\n", "needs provider and model"},
		"bad fallback":      {"model: openai:x\nfallback: [nope:y]\n", `unknown provider "nope"`},
		"bad verify model":  {"model: openai:x\nreview: {verify_model: ghost}\n", "review.verify_model"},
		"bad verify drop":   {"model: openai:x\nreview: {verify_can_drop: some}\n", "verify_can_drop"},
		"bad profile":       {"model: openai:x\nreview: {defaults: {profile: wild}}\n", "review.defaults.profile"},
		"no model":          {"models:\n  a: {provider: openai, model: x}\n  b: {provider: openai, model: y}\n", "no model selected"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Strict("inline", []byte(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestStrictExamples(t *testing.T) {
	t.Setenv("CODEREVIEW_MODEL", "")
	name, data, err := Source("../../examples/codereview.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Strict(name, data); err != nil {
		t.Fatalf("examples/codereview.yaml should pass strict validation: %v", err)
	}
}

func TestStrictPasses(t *testing.T) {
	t.Setenv("CODEREVIEW_MODEL", "")
	if _, err := Strict("inline", []byte("model: openai:x\nreview: {passes: 4}\n")); err == nil || !strings.Contains(err.Error(), "review.passes") {
		t.Errorf("passes 4: %v", err)
	}
	if _, err := Strict("inline", []byte("model: openai:x\nreview: {passes: 2}\n")); err != nil {
		t.Errorf("passes 2: %v", err)
	}
}
