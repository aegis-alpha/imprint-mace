package provider

import (
	"context"
	"errors"
	"regexp"
	"strings"
)

type ErrorClass int

const (
	ErrorTransient    ErrorClass = iota // timeout, connection refused, 502, 503, 529, 429
	ErrorAuth                          // 401, 403, invalid key
	ErrorModelNotFound                 // model deprecated/removed
	ErrorOther                         // unknown
)

func (e ErrorClass) String() string {
	switch e {
	case ErrorTransient:
		return "transient"
	case ErrorAuth:
		return "auth"
	case ErrorModelNotFound:
		return "model_not_found"
	default:
		return "other"
	}
}

var statusCodeRe = regexp.MustCompile(`status (\d{3})`)

func ClassifyError(err error) ErrorClass {
	if err == nil {
		return ErrorOther
	}

	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return ErrorTransient
	}

	msg := err.Error()
	lower := strings.ToLower(msg)

	if strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "connection reset") ||
		strings.Contains(lower, "no such host") ||
		strings.Contains(lower, "i/o timeout") {
		return ErrorTransient
	}

	if strings.Contains(lower, "model not found") ||
		strings.Contains(lower, "model_not_found") ||
		strings.Contains(lower, "does not exist") {
		return ErrorModelNotFound
	}

	if m := statusCodeRe.FindStringSubmatch(msg); len(m) == 2 {
		code := m[1]
		switch code {
		case "401", "403":
			return ErrorAuth
		case "429", "502", "503", "529":
			return ErrorTransient
		}
	}

	if strings.Contains(lower, "invalid key") ||
		strings.Contains(lower, "invalid api key") ||
		strings.Contains(lower, "unauthorized") {
		return ErrorAuth
	}

	return ErrorOther
}

func ErrorTypeFromClass(class ErrorClass, err error) string {
	msg := ""
	if err != nil {
		msg = err.Error()
	}

	if m := statusCodeRe.FindStringSubmatch(msg); len(m) == 2 {
		return "http_" + m[1]
	}

	switch class {
	case ErrorTransient:
		lower := strings.ToLower(msg)
		if strings.Contains(lower, "connection refused") {
			return "connection_refused"
		}
		return "timeout"
	case ErrorAuth:
		return "invalid_key"
	case ErrorModelNotFound:
		return "model_not_found"
	default:
		return "unknown"
	}
}

var ErrAllProvidersDown = errors.New("all providers are down; request queued for retry")
var ErrProviderUnavailable = errors.New("provider unavailable")
