package internal_test

import (
	"errors"
	"testing"
)

func TestIsProviderQuotaError_DetectsOpenAIQuotaMessage(t *testing.T) {
	err := errors.New(`extract: provider: all providers failed, last error: provider openai: status 429: {"error":{"message":"You exceeded your current quota, please check your plan and billing details.","type":"insufficient_quota"}}`)

	if !isProviderQuotaError(err) {
		t.Fatal("expected quota error to be detected")
	}
}

func TestIsProviderQuotaError_IgnoresOtherProviderFailures(t *testing.T) {
	err := errors.New(`extract: provider: all providers failed, last error: provider openai: status 500: {"error":{"message":"internal server error"}}`)

	if isProviderQuotaError(err) {
		t.Fatal("did not expect non-quota provider error to be detected as quota")
	}
}
