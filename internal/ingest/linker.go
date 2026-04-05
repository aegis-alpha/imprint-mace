// Package ingest hosts batch adapters and hot-path helpers.
//
// Hot ingest integration (Agent A / handleHotIngest): before store.InsertHotMessage, call
//
//	if err := ingest.ApplyHotLinkerRef(ctx, store, msg); err != nil { ... }
//
// ApplyHotLinkerRef sets msg.LinkerRef when empty to the ULID of the latest hot message
// from the other party in the same platform_session_id (heuristic; no LLM).
package ingest

import (
	"context"
	"errors"
	"strings"

	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/model"
)

// OtherHotSpeaker maps user <-> assistant for linker resolution. Unknown speakers yield "".
func OtherHotSpeaker(speaker string) string {
	switch strings.ToLower(strings.TrimSpace(speaker)) {
	case "user":
		return "assistant"
	case "assistant":
		return "user"
	default:
		return ""
	}
}

// ResolveHotLinkerRef returns the most recent hot message id from the other speaker in the
// same platform session, or "" when there is no prior other-party message.
func ResolveHotLinkerRef(ctx context.Context, store db.Store, platformSessionID, speaker string) (string, error) {
	other := OtherHotSpeaker(speaker)
	if other == "" || strings.TrimSpace(platformSessionID) == "" {
		return "", nil
	}
	recent, err := store.GetRecentHotMessages(ctx, platformSessionID, 500)
	if err != nil {
		return "", err
	}
	for _, m := range recent {
		if strings.EqualFold(strings.TrimSpace(m.Speaker), other) {
			return m.ID, nil
		}
	}
	return "", nil
}

// ApplyHotLinkerRef sets msg.LinkerRef using ResolveHotLinkerRef when msg.LinkerRef is empty.
// Non-empty LinkerRef is left unchanged so callers can override with platform reply metadata later.
func ApplyHotLinkerRef(ctx context.Context, store db.Store, msg *model.HotMessage) error {
	if msg == nil {
		return errors.New("ingest: nil hot message")
	}
	if strings.TrimSpace(msg.LinkerRef) != "" {
		return nil
	}
	ref, err := ResolveHotLinkerRef(ctx, store, msg.PlatformSessionID, msg.Speaker)
	if err != nil {
		return err
	}
	msg.LinkerRef = ref
	return nil
}
