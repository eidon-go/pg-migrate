package main

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestResolveVersionPrefersTheStampedValue(t *testing.T) {
	t.Parallel()

	// What a release binary looks like: -ldflags wins over everything the build
	// info could offer.
	if got := resolveVersion("v1.2.3"); got != "v1.2.3" {
		t.Errorf("resolveVersion(%q) = %q, want it unchanged", "v1.2.3", got)
	}
}

// Without a stamp the value comes from the build info. Under `go test` that is
// a working-tree build, so the result is either the module version or the VCS
// revision — never the bare "dev" that made a bug report useless.
func TestResolveVersionFallsBackToBuildInfo(t *testing.T) {
	t.Parallel()

	got := resolveVersion(devVersion)

	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Skip("no build info available")
	}

	hasVCS := false

	for _, setting := range info.Settings {
		if setting.Key == settingRevision && setting.Value != "" {
			hasVCS = true
		}
	}

	if !hasVCS {
		// Nothing to fall back to; "dev" is then the honest answer.
		if got != devVersion {
			t.Errorf("resolveVersion = %q, want %q with no VCS stamp", got, devVersion)
		}

		return
	}

	if got == devVersion {
		t.Error("resolveVersion returned \"dev\" despite a VCS revision being recorded")
	}
}

func TestRevisionFrom(t *testing.T) {
	t.Parallel()

	const fullSHA = "0123456789abcdef0123456789abcdef01234567"

	tests := []struct {
		name     string
		settings []debug.BuildSetting
		want     string
	}{
		{
			name:     "clean tree is truncated",
			settings: []debug.BuildSetting{{Key: settingRevision, Value: fullSHA}},
			want:     "0123456789ab",
		},
		{
			name: "dirty tree is marked",
			settings: []debug.BuildSetting{
				{Key: settingRevision, Value: fullSHA},
				{Key: settingModified, Value: "true"},
			},
			want: "0123456789ab-dirty",
		},
		{
			name: "explicitly unmodified is not marked",
			settings: []debug.BuildSetting{
				{Key: settingRevision, Value: fullSHA},
				{Key: settingModified, Value: "false"},
			},
			want: "0123456789ab",
		},
		{
			name:     "no revision recorded",
			settings: []debug.BuildSetting{{Key: settingGOOS, Value: "linux"}},
			want:     devVersion,
		},
		{
			name:     "short revision is left alone",
			settings: []debug.BuildSetting{{Key: settingRevision, Value: "abc123"}},
			want:     "abc123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := revisionFrom(&debug.BuildInfo{Settings: tt.settings}); got != tt.want {
				t.Errorf("revisionFrom() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildDetails(t *testing.T) {
	t.Parallel()

	got := buildDetails()
	if got == "" {
		t.Skip("no build info available")
	}

	if !strings.HasPrefix(got, " (") || !strings.HasSuffix(got, ")") {
		t.Errorf("buildDetails() = %q, want it wrapped in parentheses", got)
	}

	// The Go version is the part a bug report is actually compared against.
	if !strings.Contains(got, "go1.") {
		t.Errorf("buildDetails() = %q, want it to name the Go version", got)
	}
}
