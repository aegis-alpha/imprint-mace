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
