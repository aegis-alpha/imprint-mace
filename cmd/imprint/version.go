package main

import (
	"runtime/debug"
	"strings"
)

func resolvedVersion() string {
	return deriveVersion(version, buildInfoSettings())
}

func deriveVersion(stamped string, settings map[string]string) string {
	stamped = strings.TrimSpace(stamped)
	if stamped != "" && stamped != "dev" {
		return stamped
	}

	revision := shortRevision(settings["vcs.revision"])
	if revision == "" {
		return "dev"
	}

	v := "dev+" + revision
	if settings["vcs.modified"] == "true" {
		v += ".dirty"
	}
	return v
}

func buildInfoSettings() map[string]string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return map[string]string{}
	}
	settings := make(map[string]string, len(info.Settings))
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	return settings
}

func shortRevision(revision string) string {
	revision = strings.TrimSpace(revision)
	if len(revision) > 7 {
		return revision[:7]
	}
	return revision
}
