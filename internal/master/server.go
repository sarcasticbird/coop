package master

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/taskstore"
)

type Service struct {
	Tasks        taskstore.Backend
	BackupConfig []byte
	Store        *Store
	MCPHandler   func(c.Principal) http.Handler
}
type Envelope struct {
	MasterID     string `json:"master_id"`
	RestoreEpoch int64  `json:"restore_epoch"`
	Data         any    `json:"data"`
}

func ErrorCode(err error) string {
	for _, e := range []error{c.ErrInvalid, c.ErrForbidden, c.ErrNotFound, c.ErrConflict, c.ErrCapacity, c.ErrSize, c.ErrEpoch, c.ErrUnsupported} {
		if errors.Is(err, e) {
			return e.Error()
		}
	}
	return c.ErrUnavailable.Error()
}
func writeError(w http.ResponseWriter, err error) {
	code := ErrorCode(err)
	status := http.StatusServiceUnavailable
	switch code {
	case "unsupported_operation":
		status = http.StatusNotImplemented
	case "invalid_request":
		status = 400
	case "forbidden":
		status = 403
	case "not_found":
		status = 404
	case "conflict", "restore_reconciliation_required":
		status = 409
	case "capacity_exceeded":
		status = 429
	case "size_exceeded":
		status = 413
	}
	if status == 503 {
		slog.Error("master operation failed", "error", err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}
func decode(w http.ResponseWriter, r *http.Request, out any) error {
	r.Body = http.MaxBytesReader(w, r.Body, c.MaxJSON)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			return c.ErrSize
		}
		return c.ErrInvalid
	}
	return c.DecodeJSON(raw, out)
}
func NewHandler(service *Service, policy *Policy) http.Handler {
	s := service.Store
	mux := http.NewServeMux()
	wrap := func(fn func(http.ResponseWriter, *http.Request, c.Principal) (any, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("Content-Security-Policy", "default-src 'none'")
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
			w.Header().Set("X-Coop-Master-ID", s.id)
			w.Header().Set("X-Coop-Restore-Epoch", strconv.FormatInt(s.epoch, 10))
			if err := policy.CheckRequest(r); err != nil {
				writeError(w, err)
				return
			}
			entry, err := policy.authenticateEntry(bearer(r.Header))
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(401)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
				return
			}
			p, err := s.Principal(r.Context(), entry.PrincipalID)
			if err != nil {
				writeError(w, err)
				return
			}
			pins := r.Header.Values("X-Coop-Expected-Master-ID")
			if len(pins) != 1 || pins[0] == "" {
				writeError(w, c.ErrInvalid)
				return
			}
			if pins[0] != s.id {
				writeError(w, c.ErrConflict)
				return
			}
			if p.Kind == "agent" {
				if err = placement(r.Context(), s.db, p.ID, entry.InstanceID, entry.Generation); err != nil {
					writeError(w, c.ErrForbidden)
					return
				}
			}
			v, err := fn(w, r, p)
			if err != nil {
				writeError(w, err)
				return
			}
			if v == nil {
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if err = json.NewEncoder(w).Encode(Envelope{MasterID: s.id, RestoreEpoch: s.epoch, Data: v}); err != nil {
				slog.Error("write master response", "error", err)
			}
		}
	}
	registerGitHubCache(mux, service, wrap)
	registerTasks(mux, service, wrap)
	mux.HandleFunc("GET /api/v1/deliveries/{id}", wrap(func(_ http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		return s.OwnDelivery(r.Context(), p, r.PathValue("id"))
	}))
	mux.HandleFunc("GET /api/v1/identity", wrap(func(_ http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		if _, err := authorize(r.Context(), s.db, p, "read"); err != nil {
			return nil, err
		}
		out := c.SelfIdentity{Principal: p, RestoreEpoch: s.epoch}
		if p.Kind == "agent" {
			var raw []byte
			if err := s.db.QueryRowContext(r.Context(), "SELECT body FROM placements WHERE agent_id=?", p.ID).Scan(&raw); err != nil {
				return nil, err
			}
			if err := json.Unmarshal(raw, &out.Placement); err != nil {
				return nil, err
			}
		}
		return out, nil
	}))
	mux.HandleFunc("GET /api/v1/goals", wrap(func(_ http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		return service.ApprovedGoals(r.Context(), p, r.URL.Query().Get("engagement"), r.URL.Query().Get("after"))
	}))
	mux.HandleFunc("POST /api/v1/artifacts", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.ArtifactLink
		if err := decode(w, r, &in); err != nil {
			return nil, err
		}
		return service.RegisterArtifact(r.Context(), p, in)
	}))
	mux.HandleFunc("POST /api/v1/artifacts/list", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.ArtifactQuery
		if err := decode(w, r, &in); err != nil {
			return nil, err
		}
		return service.Artifacts(r.Context(), p, in)
	}))
	mux.HandleFunc("POST /api/v1/ping", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.Ping
		if err := decode(w, r, &in); err != nil {
			return nil, err
		}
		return s.Ping(r.Context(), p, in)
	}))
	mux.HandleFunc("GET /api/v1/team", wrap(func(_ http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		return s.Team(r.Context(), p)
	}))
	mux.HandleFunc("GET /api/v1/team/{agent_id}/diagnostics", wrap(func(_ http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		return s.AgentDiagnostics(r.Context(), p, r.PathValue("agent_id"))
	}))
	mux.HandleFunc("POST /api/v1/diagnostics", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, c.MaxDiagnosticsBytes))
		if err != nil {
			return nil, c.ErrSize
		}
		var in c.WorkerDiagnostics
		if err = c.DecodeJSON(raw, &in); err != nil {
			return nil, err
		}
		err = s.ReportDiagnostics(r.Context(), p, in)
		return map[string]bool{"ok": err == nil}, err
	}))
	mux.HandleFunc("POST /api/v1/goals", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.GoalProposal
		if err := decode(w, r, &in); err != nil {
			return nil, err
		}
		return service.ProposeGoal(r.Context(), p, in)
	}))
	mux.HandleFunc("POST /api/v1/goal-decisions", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.GoalDecisionInput
		if err := decode(w, r, &in); err != nil {
			return nil, err
		}
		return service.DecideGoal(r.Context(), p, in)
	}))
	mux.HandleFunc("POST /api/v1/goal-authority", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.GoalRef
		if err := decode(w, r, &in); err != nil {
			return nil, err
		}
		return service.ResolveGoal(r.Context(), p, in)
	}))
	mux.HandleFunc("GET /api/v1/threads", wrap(func(_ http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		return s.Threads(r.Context(), p, c.ThreadQuery{After: r.URL.Query().Get("after")})
	}))
	mux.HandleFunc("GET /api/v1/threads/{id}", wrap(func(_ http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		return s.Thread(r.Context(), p, r.PathValue("id"))
	}))
	mux.HandleFunc("POST /api/v1/thread-members", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.SetMembers
		if err := decode(w, r, &in); err != nil {
			return nil, err
		}
		return s.SetMembers(r.Context(), p, in)
	}))
	mux.HandleFunc("POST /api/v1/threads", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.CreateThread
		if err := decode(w, r, &in); err != nil {
			return nil, err
		}
		return s.CreateThread(r.Context(), p, in)
	}))
	mux.HandleFunc("GET /api/v1/threads/{id}/messages", wrap(func(_ http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		after := int64(0)
		if v := r.URL.Query().Get("after"); v != "" {
			var err error
			after, err = strconv.ParseInt(v, 10, 64)
			if err != nil || after < 0 {
				return nil, c.ErrInvalid
			}
		}
		return s.Messages(r.Context(), p, r.PathValue("id"), after)
	}))
	mux.HandleFunc("POST /api/v1/messages", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.SendMessage
		if err := decode(w, r, &in); err != nil {
			return nil, err
		}
		return s.Send(r.Context(), p, in)
	}))
	mux.HandleFunc("GET /api/v1/messages/{id}", wrap(func(_ http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		return s.Message(r.Context(), p, r.PathValue("id"))
	}))
	mux.HandleFunc("GET /api/v1/messages/{id}/continuation", wrap(func(_ http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		return s.Continuation(r.Context(), p, r.PathValue("id"))
	}))
	mux.HandleFunc("GET /api/v1/messages/{id}/status", wrap(func(_ http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		return s.MessageStatus(r.Context(), p, r.PathValue("id"))
	}))
	mux.HandleFunc("GET /api/v1/pending", wrap(func(_ http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		return s.Pending(r.Context(), p, c.PendingQuery{After: r.URL.Query().Get("after")})
	}))
	mux.HandleFunc("POST /api/v1/deliveries", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.DeliveryUpdate
		if err := decode(w, r, &in); err != nil {
			return nil, err
		}
		err := s.SetDelivery(r.Context(), p, in)
		return map[string]bool{"ok": err == nil}, err
	}))
	mux.HandleFunc("POST /api/v1/replies", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.PublishReply
		if err := decode(w, r, &in); err != nil {
			return nil, err
		}
		return s.PublishReply(r.Context(), p, in)
	}))
	mux.HandleFunc("POST /api/v1/heartbeat", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.Heartbeat
		if err := decode(w, r, &in); err != nil {
			return nil, err
		}
		err := s.Heartbeat(r.Context(), p, in)
		return map[string]bool{"ok": err == nil}, err
	}))
	mux.HandleFunc("GET /events", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		serveEvents(w, r, s, p, func() error {
			entry, err := policy.authenticateEntry(bearer(r.Header))
			if err != nil {
				return err
			}
			if entry.PrincipalID != p.ID {
				return c.ErrForbidden
			}
			if p.Kind == "agent" {
				return placement(r.Context(), s.db, p.ID, entry.InstanceID, entry.Generation)
			}
			return nil
		})
		return nil, nil
	}))
	mux.HandleFunc("/mcp", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		if service.MCPHandler == nil {
			return nil, c.ErrNotFound
		}
		r.Body = http.MaxBytesReader(w, r.Body, c.MaxJSON)
		service.MCPHandler(p).ServeHTTP(w, r)
		return nil, nil
	}))
	recoveryRoutes(mux, service, wrap)
	return mux
}
