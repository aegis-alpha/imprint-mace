package provider

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
)

func TestClassifyError_Timeout(t *testing.T) {
	err := context.DeadlineExceeded
	if got := ClassifyError(err); got != ErrorTransient {
		t.Errorf("expected ErrorTransient for timeout, got %v", got)
	}
}

func TestClassifyError_ConnectionRefused(t *testing.T) {
	err := &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	if got := ClassifyError(err); got != ErrorTransient {
		t.Errorf("expected ErrorTransient for connection refused, got %v", got)
	}
}

func TestClassifyError_HTTP401(t *testing.T) {
	err := fmt.Errorf("status 401: unauthorized")
	if got := ClassifyError(err); got != ErrorAuth {
		t.Errorf("expected ErrorAuth for 401, got %v", got)
	}
}

func TestClassifyError_HTTP403(t *testing.T) {
	err := fmt.Errorf("status 403: forbidden")
	if got := ClassifyError(err); got != ErrorAuth {
		t.Errorf("expected ErrorAuth for 403, got %v", got)
	}
}

func TestClassifyError_HTTP502(t *testing.T) {
	err := fmt.Errorf("status 502: bad gateway")
	if got := ClassifyError(err); got != ErrorTransient {
		t.Errorf("expected ErrorTransient for 502, got %v", got)
	}
}

func TestClassifyError_HTTP503(t *testing.T) {
	err := fmt.Errorf("status 503: service unavailable")
	if got := ClassifyError(err); got != ErrorTransient {
		t.Errorf("expected ErrorTransient for 503, got %v", got)
	}
}

func TestClassifyError_HTTP529(t *testing.T) {
	err := fmt.Errorf("status 529: overloaded")
	if got := ClassifyError(err); got != ErrorTransient {
		t.Errorf("expected ErrorTransient for 529, got %v", got)
	}
}

func TestClassifyError_ModelNotFound(t *testing.T) {
	err := fmt.Errorf("model not found: gpt-5-nano")
	if got := ClassifyError(err); got != ErrorModelNotFound {
		t.Errorf("expected ErrorModelNotFound, got %v", got)
	}
}

func TestClassifyError_Unknown(t *testing.T) {
	err := fmt.Errorf("some random error")
	if got := ClassifyError(err); got != ErrorOther {
		t.Errorf("expected ErrorOther, got %v", got)
	}
}

func TestClassifyError_WrappedTimeout(t *testing.T) {
	err := fmt.Errorf("provider openai: %w", context.DeadlineExceeded)
	if got := ClassifyError(err); got != ErrorTransient {
		t.Errorf("expected ErrorTransient for wrapped timeout, got %v", got)
	}
}

func TestClassifyError_HTTP429(t *testing.T) {
	err := fmt.Errorf("status 429: rate limited")
	if got := ClassifyError(err); got != ErrorTransient {
		t.Errorf("expected ErrorTransient for 429, got %v", got)
	}
}

func TestErrorTypeFromClass_HTTPStatus(t *testing.T) {
	err := fmt.Errorf("status 503: service unavailable")
	if got := ErrorTypeFromClass(ErrorTransient, err); got != "http_503" {
		t.Errorf("expected http_503, got %s", got)
	}
}

func TestErrorTypeFromClass_ConnectionRefused(t *testing.T) {
	err := fmt.Errorf("connection refused")
	if got := ErrorTypeFromClass(ErrorTransient, err); got != "connection_refused" {
		t.Errorf("expected connection_refused, got %s", got)
	}
}

func TestErrorTypeFromClass_Timeout(t *testing.T) {
	err := fmt.Errorf("context deadline exceeded")
	if got := ErrorTypeFromClass(ErrorTransient, err); got != "timeout" {
		t.Errorf("expected timeout, got %s", got)
	}
}

func TestErrorTypeFromClass_Auth(t *testing.T) {
	err := fmt.Errorf("invalid api key")
	if got := ErrorTypeFromClass(ErrorAuth, err); got != "invalid_key" {
		t.Errorf("expected invalid_key, got %s", got)
	}
}

func TestErrorTypeFromClass_ModelNotFound(t *testing.T) {
	err := fmt.Errorf("model not found")
	if got := ErrorTypeFromClass(ErrorModelNotFound, err); got != "model_not_found" {
		t.Errorf("expected model_not_found, got %s", got)
	}
}

func TestErrorTypeFromClass_Unknown(t *testing.T) {
	err := fmt.Errorf("something weird")
	if got := ErrorTypeFromClass(ErrorOther, err); got != "unknown" {
		t.Errorf("expected unknown, got %s", got)
	}
}

func TestErrorTypeFromClass_NilError(t *testing.T) {
	if got := ErrorTypeFromClass(ErrorOther, nil); got != "unknown" {
		t.Errorf("expected unknown for nil error, got %s", got)
	}
}
