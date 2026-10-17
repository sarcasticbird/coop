package worker

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// ModelIdentity describes a submitted selection or native assistant metadata,
// not independent evidence of the upstream provider's reasoning settings.
type ModelIdentity struct {
	ProviderID string `json:"providerID"`
	ModelID    string `json:"modelID"`
	Variant    string `json:"variant,omitempty"`
}

func configuredModel(cfg Config) (*ModelIdentity, error) {
	model := strings.Split(cfg.Model, "/")
	if len(model) != 2 || model[0] == "" || model[1] == "" || strings.ContainsAny(cfg.Model, "\x00\r\n\t ") || !validModelVariant(cfg.ModelVariant) {
		return nil, &OpenCodeError{Category: "invalid_model"}
	}
	return &ModelIdentity{ProviderID: model[0], ModelID: model[1], Variant: cfg.ModelVariant}, nil
}

func modelPrompt(model *ModelIdentity) map[string]any {
	body := map[string]any{"agent": "build", "model": map[string]string{"providerID": model.ProviderID, "modelID": model.ModelID}}
	// OpenCode v1.18.18 PromptInput has a top-level variant, not model.variant.
	if model.Variant != "" {
		body["variant"] = model.Variant
	}
	return body
}

// These deliberately narrow schema types fail closed if the deployed API no
// longer matches the verified v1.18.18 schema. Do not guess alternate fields.
type modelSchema struct {
	Type       string                 `json:"type"`
	Properties map[string]modelSchema `json:"properties"`
}

func (c *OpenCodeClient) preflightModelVariant(ctx context.Context, cfg Config) error {
	return c.preflightModelVariantInDirectory(ctx, cfg, cfg.WorkRoot)
}

func (c *OpenCodeClient) preflightModelVariantInDirectory(ctx context.Context, cfg Config, directory string) (result error) {
	if cfg.ModelVariant == "" {
		return nil
	}
	model, err := configuredModel(cfg)
	if err != nil {
		return err
	}
	if _, err := c.Health(ctx); err != nil {
		return err
	}
	var doc struct {
		Paths map[string]map[string]struct {
			RequestBody struct {
				Content map[string]struct{ Schema modelSchema }
			}
		}
		Components struct{ Schemas map[string]modelSchema }
	}
	if err := c.request(ctx, "GET", "/doc", "", nil, &doc); err != nil {
		return err
	}
	props := doc.Paths["/session/{sessionID}/prompt_async"]["post"].RequestBody.Content["application/json"].Schema.Properties
	if props["variant"].Type != "string" || props["noReply"].Type != "boolean" {
		return &OpenCodeError{Category: "unsupported_model_variant_API"}
	}
	if doc.Components.Schemas["UserMessage"].Properties["model"].Properties["variant"].Type != "string" || doc.Components.Schemas["AssistantMessage"].Properties["variant"].Type != "string" {
		return &OpenCodeError{Category: "unsupported_model_variant_API"}
	}
	var providers struct {
		Providers []struct {
			ID     string
			Models map[string]struct{ Variants map[string]json.RawMessage }
		}
	}
	if err := c.request(ctx, "GET", "/config/providers", directory, nil, &providers); err != nil {
		return err
	}
	available := false
	for _, p := range providers.Providers {
		if p.ID == model.ProviderID {
			raw := p.Models[model.ModelID].Variants[model.Variant]
			var options map[string]json.RawMessage
			available = json.Unmarshal(raw, &options) == nil && options != nil
		}
	}
	if !available {
		return &OpenCodeError{Category: "configured_model_variant_unavailable"}
	}
	// noReply returns the persisted user message before the LLM loop in the
	// verified source. Use a disposable session, never a worker/manual session.
	var session openCodeSession
	if err := c.request(ctx, "POST", "/session", directory, map[string]any{"title": "coop-model-preflight", "permission": []PermissionRule{{Permission: "*", Pattern: "*", Action: "deny"}}}, &session); err != nil {
		return err
	}
	if !validSession(session.ID) || session.Directory != directory || session.ParentID != "" || session.Title != "coop-model-preflight" || session.Time.Archived != 0 {
		return &OpenCodeError{Category: "foreign_session"}
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := c.request(cleanup, "DELETE", "/session/"+session.ID, directory, nil, nil); err != nil && result == nil {
			result = &OpenCodeError{Category: "model_preflight_cleanup_failed"}
		}
	}()
	body := modelPrompt(model)
	messageID := "msg_" + randomID()
	body["messageID"] = messageID
	body["noReply"] = true
	body["parts"] = []map[string]string{{"type": "text", "text": "Coop model selection preflight; no assistant reply requested."}}
	if err := c.request(ctx, "POST", "/session/"+session.ID+"/prompt_async", directory, body, nil); err != nil {
		return err
	}
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		var message messageEnvelope
		err := c.request(probe, "GET", "/session/"+session.ID+"/message/"+messageID, directory, nil, &message)
		if err == nil {
			if message.Info.SessionID != session.ID || message.Info.ID != messageID || message.Info.Role != "user" || message.Info.Model == nil || *message.Info.Model != *model {
				return &OpenCodeError{Category: "model_variant_not_preserved"}
			}
			return nil
		}
		if api, ok := err.(*OpenCodeError); !ok || api.Status != 404 {
			return err
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-probe.Done():
			timer.Stop()
			return &OpenCodeError{Category: "model_variant_probe_unconfirmed"}
		case <-timer.C:
		}
	}
}
