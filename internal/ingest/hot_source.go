package ingest

import "strings"

// ParseHotIngestSource maps ingest source strings to platform and session id for hot-path storage.
// A source prefixed with "realtime:" uses the remainder as platform_session_id; platform defaults to "unknown".
func ParseHotIngestSource(source string) (platform, platformSessionID string) {
	platform = "unknown"
	if strings.HasPrefix(source, "realtime:") {
		return platform, strings.TrimPrefix(source, "realtime:")
	}
	return platform, ""
}
