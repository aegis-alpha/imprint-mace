package ingest

import "testing"

func TestParseHotIngestSource(t *testing.T) {
	p, sid := ParseHotIngestSource("realtime:session-abc")
	if p != "unknown" || sid != "session-abc" {
		t.Fatalf("realtime: got platform=%q sid=%q", p, sid)
	}
	p, sid = ParseHotIngestSource("api")
	if p != "unknown" || sid != "" {
		t.Fatalf("api: got platform=%q sid=%q", p, sid)
	}
}
