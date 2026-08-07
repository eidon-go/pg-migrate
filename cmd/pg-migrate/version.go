package main

import (
	"runtime/debug"
	"strings"
)

// devVersion is what `version` holds when nothing was stamped in.
const devVersion = "dev"

// shortRevisionLength matches what git log --oneline shows, which is what a bug
// report will be compared against.
const shortRevisionLength = 12

// Build settings the Go toolchain records, named here because both the
// implementation and its tests refer to them.
const (
	settingRevision = "vcs.revision"
	settingModified = "vcs.modified"
	settingGOOS     = "GOOS"
	settingGOARCH   = "GOARCH"
)

// resolveVersion reports the version to print, preferring the most specific
// source available.
//
// Release binaries have it stamped in with -ldflags. `go install module@vX.Y.Z`
// does not run those, so the value would stay "dev" and a bug report would say
// nothing about which code is running — the build info carries the module
// version in that case. A build from a working tree has neither, so it falls
// back to the VCS revision the toolchain records.
func resolveVersion(stamped string) string {
	if stamped != "" && stamped != devVersion {
		return stamped
	}

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return devVersion
	}

	// "(devel)" is what the toolchain reports for a build that is not from a
	// module version — no more useful than "dev".
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}

	return revisionFrom(info)
}

// revisionFrom renders the VCS stamp the toolchain embeds for builds from a
// working tree, as "abc123def456" or "abc123def456-dirty".
func revisionFrom(info *debug.BuildInfo) string {
	var revision, modified string

	for _, setting := range info.Settings {
		switch setting.Key {
		case settingRevision:
			revision = setting.Value
		case settingModified:
			modified = setting.Value
		}
	}

	if revision == "" {
		return devVersion
	}

	if len(revision) > shortRevisionLength {
		revision = revision[:shortRevisionLength]
	}

	if modified == "true" {
		return revision + "-dirty"
	}

	return revision
}

// buildDetails renders the Go toolchain and platform, appended to the version
// line because "which Go built this" is the second question after "which
// version" when a report comes in.
func buildDetails() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}

	var goos, goarch string

	for _, setting := range info.Settings {
		switch setting.Key {
		case settingGOOS:
			goos = setting.Value
		case settingGOARCH:
			goarch = setting.Value
		}
	}

	parts := make([]string, 0, 2)
	if info.GoVersion != "" {
		parts = append(parts, info.GoVersion)
	}

	if goos != "" && goarch != "" {
		parts = append(parts, goos+"/"+goarch)
	}

	if len(parts) == 0 {
		return ""
	}

	return " (" + strings.Join(parts, " ") + ")"
}
