package provider

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/db"
)

type HealthChecker struct {
	store   db.Store
	listers []ModelLister
	configs map[string]map[string]string // provider -> task -> configured_model
	logger  *slog.Logger
}

func NewHealthChecker(store db.Store, listers []ModelLister, configs map[string]map[string]string, logger *slog.Logger) *HealthChecker {
	return &HealthChecker{store: store, listers: listers, configs: configs, logger: logger}
}

func (h *HealthChecker) CheckAll(ctx context.Context) error {
	now := time.Now().UTC().Truncate(time.Second)

	availableByProvider := make(map[string]map[string]bool)
	modelListByProvider := make(map[string][]string)

	for _, lister := range h.listers {
		name := lister.ProviderName()
		models, err := lister.ListModels(ctx)
		if err != nil {
			h.logger.Error("model list failed", "provider", name, "error", err)
			for task, configuredModel := range h.configs[name] {
				if uErr := h.store.UpsertProviderHealth(ctx, &db.ProviderHealth{
					ProviderName:    name,
					TaskType:        task,
					ConfiguredModel: configuredModel,
					ActiveModel:     "",
					Status:          "unavailable",
					LastError:       err.Error(),
					LastChecked:     now,
				}); uErr != nil {
					return uErr
				}
			}
			continue
		}

		available := make(map[string]bool, len(models))
		for _, m := range models {
			available[m.ID] = true
			if uErr := h.store.UpsertProviderModel(ctx, &db.ProviderModel{
				ProviderName:  name,
				ModelID:       m.ID,
				ContextWindow: m.ContextWindow,
				Available:     true,
				LastChecked:   now,
			}); uErr != nil {
				return uErr
			}
		}
		availableByProvider[name] = available

		ids := make([]string, len(models))
		for i, m := range models {
			ids[i] = m.ID
		}
		modelListByProvider[name] = ids

		h.logger.Info("health check complete", "provider", name, "models_found", len(models))
	}

	for providerName, tasks := range h.configs {
		available := availableByProvider[providerName]
		if available == nil {
			continue
		}

		models := modelListByProvider[providerName]

		for task, configuredModel := range tasks {
			status := "ok"
			activeModel := configuredModel
			lastError := ""

			if !available[configuredModel] {
				status = "degraded"
				activeModel = findSubstitute(configuredModel, models)
				if activeModel == "" {
					lastError = "model not found in provider model list, no substitute available"
				} else {
					lastError = "configured model not found, using substitute: " + activeModel
				}
			}

			if err := h.store.UpsertProviderHealth(ctx, &db.ProviderHealth{
				ProviderName:    providerName,
				TaskType:        task,
				ConfiguredModel: configuredModel,
				ActiveModel:     activeModel,
				Status:          status,
				LastError:       lastError,
				LastChecked:     now,
			}); err != nil {
				return err
			}

			h.logger.Info("provider health", "provider", providerName, "task", task,
				"status", status, "active_model", activeModel)
		}
	}

	return nil
}

func findSubstitute(configured string, available []string) string {
	parts := strings.Split(configured, "-")
	for prefixLen := len(parts) - 1; prefixLen >= 1; prefixLen-- {
		prefix := strings.Join(parts[:prefixLen], "-")
		for _, m := range available {
			if strings.HasPrefix(m, prefix) {
				return m
			}
		}
	}

	if len(available) > 0 {
		return available[0]
	}
	return ""
}
