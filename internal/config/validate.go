package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/divmora/code-review-bot/internal/llm"
)

// Strict parses a config like Load but rejects unknown keys, so a typo or a
// key this version does not support is reported instead of ignored. Then it
// runs Check.
func Strict(name string, data []byte) (*Config, error) {
	cfg := &Config{}
	if len(bytes.TrimSpace(data)) > 0 {
		dec := yaml.NewDecoder(bytes.NewReader(ExpandEnv(data)))
		dec.KnownFields(true)
		if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("parse %s: %w", sourceLabel(name), err)
		}
	}
	cfg.Finalize()
	return cfg, cfg.Check()
}

// Check validates what a review would otherwise only find out at run time:
// model profiles, the model chain, the verify model and review settings. It
// never contacts a provider (use "codereview ping" for credentials).
func (c *Config) Check() error {
	var errs []error
	names := make([]string, 0, len(c.Models))
	for n := range c.Models {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		m := c.Models[n]
		if m.Provider == "" || m.Model == "" {
			errs = append(errs, fmt.Errorf("model profile %q needs provider and model", n))
			continue
		}
		if !llm.Known(m.Provider) {
			errs = append(errs, fmt.Errorf("model profile %q: unknown provider %q (available: %v)", n, m.Provider, llm.Providers()))
		}
	}
	checkRef := func(what, ref string) {
		m, err := c.ResolveModel(ref)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", what, err))
			return
		}
		if _, profile := c.Models[ref]; !profile && !llm.Known(m.Provider) {
			errs = append(errs, fmt.Errorf("%s %q: unknown provider %q", what, ref, m.Provider))
		}
	}
	if _, err := c.ModelChain(""); err != nil && c.Model == "" {
		errs = append(errs, err)
	} else {
		if c.Model != "" {
			checkRef("model", c.Model)
		}
		for _, f := range c.Fallback {
			checkRef("fallback", f)
		}
	}
	if c.Review.VerifyModel != "" {
		checkRef("review.verify_model", c.Review.VerifyModel)
	}
	if c.Review.Passes < 0 || c.Review.Passes > 3 {
		errs = append(errs, fmt.Errorf("review.passes %d: want 1, 2 or 3", c.Review.Passes))
	}
	switch c.Review.VerifyCanDrop {
	case "", "confident", "minor", "all":
	default:
		errs = append(errs, fmt.Errorf("review.verify_can_drop %q: want confident, minor or all", c.Review.VerifyCanDrop))
	}
	switch c.Review.Defaults.Profile {
	case "", Chill, Balanced, Assertive:
	default:
		errs = append(errs, fmt.Errorf("review.defaults.profile %q: want chill, balanced or assertive", c.Review.Defaults.Profile))
	}
	if err := CheckSeverity("review.defaults.fail_on", c.Review.Defaults.FailOn); err != nil {
		errs = append(errs, err)
	}
	errs = append(errs, c.Review.Approve.Check("review.approve")...)
	return errors.Join(errs...)
}
