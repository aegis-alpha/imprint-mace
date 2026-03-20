package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/model"
)

type ResolvedProvider struct {
	inner        Provider
	store        db.Store
	providerName string
	taskType     string
}

func NewResolvedProvider(inner Provider, store db.Store, providerName, taskType string) *ResolvedProvider {
	return &ResolvedProvider{
		inner:        inner,
		store:        store,
		providerName: providerName,
		taskType:     taskType,
	}
}

func (rp *ResolvedProvider) Name() string { return rp.providerName }

func (rp *ResolvedProvider) ActiveModel() string {
	h, err := rp.store.GetProviderHealth(context.Background(), rp.providerName, rp.taskType)
	if err != nil {
		return ""
	}
	if h.ActiveModel != "" {
		return h.ActiveModel
	}
	return h.ConfiguredModel
}

func (rp *ResolvedProvider) Send(ctx context.Context, req Request) (*Response, error) {
	ops, err := rp.store.GetProviderOps(ctx, rp.providerName)
	if err != nil && err != db.ErrNotFound {
		return nil, fmt.Errorf("check provider ops: %w", err)
	}

	if ops != nil {
		if ops.Status == "auth_error" {
			return nil, fmt.Errorf("provider %s: %w (auth_error: %s)", rp.providerName, ErrProviderUnavailable, ops.LastError)
		}

		if ops.Status == "transient_error" && ops.NextCheckAt != nil {
			if time.Now().UTC().Before(*ops.NextCheckAt) {
				return nil, fmt.Errorf("provider %s: %w (transient_error, retry after %s)",
					rp.providerName, ErrProviderUnavailable, ops.NextCheckAt.Format(time.RFC3339))
			}
		}
	}

	resp, sendErr := rp.inner.Send(ctx, req)
	if sendErr == nil {
		rp.recordSuccess(ctx)
		return resp, nil
	}

	rp.recordError(ctx, sendErr, ops)
	return nil, sendErr
}

func (rp *ResolvedProvider) recordSuccess(ctx context.Context) {
	now := time.Now().UTC().Truncate(time.Second)
	_ = rp.store.UpsertProviderOps(ctx, &db.ProviderOps{
		ProviderName: rp.providerName,
		Status:       "ok",
		RetryCount:   0,
		MaxRetries:   5,
		LastSuccess:  &now,
		CreatedAt:    now,
		UpdatedAt:    now,
	})
}

func (rp *ResolvedProvider) recordError(ctx context.Context, sendErr error, existing *db.ProviderOps) {
	now := time.Now().UTC().Truncate(time.Second)
	class := ClassifyError(sendErr)
	errType := ErrorTypeFromClass(class, sendErr)

	retryCount := 1
	maxRetries := 5
	if existing != nil {
		retryCount = existing.RetryCount + 1
		maxRetries = existing.MaxRetries
		if maxRetries == 0 {
			maxRetries = 5
		}
	}

	status := "transient_error"
	var nextCheck *time.Time

	switch class {
	case ErrorAuth:
		status = "auth_error"
	case ErrorTransient:
		if retryCount >= maxRetries {
			status = "exhausted"
		}
		nc := now.Add(time.Hour)
		nextCheck = &nc
	default:
		nc := now.Add(time.Hour)
		nextCheck = &nc
	}

	_ = rp.store.UpsertProviderOps(ctx, &db.ProviderOps{
		ProviderName: rp.providerName,
		Status:       status,
		RetryCount:   retryCount,
		MaxRetries:   maxRetries,
		LastError:    sendErr.Error(),
		ErrorType:    errType,
		NextCheckAt:  nextCheck,
		CreatedAt:    now,
		UpdatedAt:    now,
	})

	if status == "exhausted" || status == "auth_error" {
		rp.writeProviderFact(ctx, status, sendErr, retryCount)
	}
}

func (rp *ResolvedProvider) writeProviderFact(ctx context.Context, status string, lastErr error, retryCount int) {
	now := time.Now().UTC()
	validUntil := now.Add(24 * time.Hour)

	content := fmt.Sprintf("Provider %s has been unreachable for %d retries. Last error: %s.",
		rp.providerName, retryCount, lastErr)
	if status == "auth_error" {
		content = fmt.Sprintf("Provider %s has an authentication error. Last error: %s. Manual config fix required.",
			rp.providerName, lastErr)
	}

	_ = rp.store.CreateFact(ctx, &model.Fact{
		ID:       db.NewID(),
		FactType: "context",
		Subject:  "provider-health",
		Content:  content,
		Confidence: 1.0,
		Source: model.Source{
			TranscriptFile: "system:provider-health",
		},
		Validity: model.TimeRange{
			ValidFrom:  &now,
			ValidUntil: &validUntil,
		},
		CreatedAt: now,
	})
}

type ResolvedEmbedder struct {
	inner        Embedder
	store        db.Store
	providerName string
}

func NewResolvedEmbedder(inner Embedder, store db.Store, providerName string) *ResolvedEmbedder {
	return &ResolvedEmbedder{inner: inner, store: store, providerName: providerName}
}

func (re *ResolvedEmbedder) ModelName() string { return re.inner.ModelName() }

func (re *ResolvedEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	ops, err := re.store.GetProviderOps(ctx, re.providerName)
	if err != nil && err != db.ErrNotFound {
		return nil, fmt.Errorf("check provider ops: %w", err)
	}

	if ops != nil {
		if ops.Status == "auth_error" {
			return nil, fmt.Errorf("embedder %s: %w (auth_error)", re.providerName, ErrProviderUnavailable)
		}
		if ops.Status == "transient_error" && ops.NextCheckAt != nil {
			if time.Now().UTC().Before(*ops.NextCheckAt) {
				return nil, fmt.Errorf("embedder %s: %w (transient_error)", re.providerName, ErrProviderUnavailable)
			}
		}
	}

	vec, embedErr := re.inner.Embed(ctx, text)
	if embedErr == nil {
		now := time.Now().UTC().Truncate(time.Second)
		_ = re.store.UpsertProviderOps(ctx, &db.ProviderOps{
			ProviderName: re.providerName,
			Status:       "ok",
			RetryCount:   0,
			MaxRetries:   5,
			LastSuccess:  &now,
			CreatedAt:    now,
			UpdatedAt:    now,
		})
		return vec, nil
	}

	now := time.Now().UTC().Truncate(time.Second)
	class := ClassifyError(embedErr)
	errType := ErrorTypeFromClass(class, embedErr)

	retryCount := 1
	if ops != nil {
		retryCount = ops.RetryCount + 1
	}

	status := "transient_error"
	if class == ErrorAuth {
		status = "auth_error"
	}
	nc := now.Add(time.Hour)

	_ = re.store.UpsertProviderOps(ctx, &db.ProviderOps{
		ProviderName: re.providerName,
		Status:       status,
		RetryCount:   retryCount,
		MaxRetries:   5,
		LastError:    embedErr.Error(),
		ErrorType:    errType,
		NextCheckAt:  &nc,
		CreatedAt:    now,
		UpdatedAt:    now,
	})

	return nil, embedErr
}
