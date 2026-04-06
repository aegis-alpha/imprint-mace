package internal_test

import (
	"strings"
	"testing"
)

func isProviderQuotaError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "status 429") {
		return false
	}
	return strings.Contains(msg, "insufficient_quota") ||
		strings.Contains(msg, "exceeded your current quota") ||
		strings.Contains(msg, "billing details")
}

func maybeSkipProviderQuota(t *testing.T, phase string, err error) {
	t.Helper()
	if isProviderQuotaError(err) {
		t.Skipf("%s skipped: provider quota exhausted: %v", phase, err)
	}
}
