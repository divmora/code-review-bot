// Package version holds the build version, injected via -ldflags.
package version

// Version is set at build time: -ldflags "-X github.com/divmora/code-review-bot/internal/version.Version=1.2.3".
var Version = "0.4.1" // x-release-please-version
