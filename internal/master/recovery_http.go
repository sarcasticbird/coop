package master

import (
	c "github.com/sarcasticbird/coop/internal/coordination"
	"net/http"
)

func recoveryRoutes(mux *http.ServeMux, svc *Service, wrap func(func(http.ResponseWriter, *http.Request, c.Principal) (any, error)) http.HandlerFunc) {
	s := svc.Store
	mux.HandleFunc("GET /api/v1/placement/transition", wrap(func(_ http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		return s.PlacementProof(r.Context(), p)
	}))
	mux.HandleFunc("POST /api/v1/backup", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		if e := operator(r.Context(), s.db, p); e != nil {
			return nil, e
		}
		var in struct {
			Directory string `json:"directory"`
		}
		if e := decode(w, r, &in); e != nil {
			return nil, e
		}
		return s.Backup(r.Context(), in.Directory, svc.BackupConfig)
	}))
	mux.HandleFunc("GET /api/v1/recovery", wrap(func(_ http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		return s.RecoveryStatus(r.Context(), p)
	}))
	mux.HandleFunc("GET /api/v1/recovery/evidence", wrap(func(_ http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		return s.RecoveryEvidence(r.Context(), p, r.URL.Query().Get("agent"), r.URL.Query().Get("after"))
	}))
	mux.HandleFunc("POST /api/v1/recovery/report", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.GuestRecovery
		if e := decode(w, r, &in); e != nil {
			return nil, e
		}
		return s.ReconcileGuest(r.Context(), p, in)
	}))
	mux.HandleFunc("POST /api/v1/recovery/resolve", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.RecoveryResolution
		if e := decode(w, r, &in); e != nil {
			return nil, e
		}
		e := s.ResolveRecovery(r.Context(), p, in)
		return map[string]bool{"ok": e == nil}, e
	}))
	for _, op := range []string{"pause", "resume"} {
		mux.HandleFunc("POST /api/v1/recovery/"+op, wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
			var in struct {
				Epoch int64 `json:"restore_epoch"`
			}
			if e := decode(w, r, &in); e != nil {
				return nil, e
			}
			var e error
			if op == "pause" {
				e = s.Quiesce(r.Context(), p, in.Epoch)
			} else {
				e = s.Resume(r.Context(), p, in.Epoch)
			}
			return map[string]bool{"ok": e == nil}, e
		}))
	}
	mux.HandleFunc("POST /api/v1/placement", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.PlacementChange
		if e := decode(w, r, &in); e != nil {
			return nil, e
		}
		return s.SetPlacement(r.Context(), p, in)
	}))
	mux.HandleFunc("POST /api/v1/history", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.HistoryImport
		if e := decode(w, r, &in); e != nil {
			return nil, e
		}
		return s.ImportHistory(r.Context(), p, in)
	}))
	mux.HandleFunc("GET /api/v1/history", wrap(func(_ http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		return s.History(r.Context(), p, r.URL.Query().Get("thread"), r.URL.Query().Get("after"))
	}))
	mux.HandleFunc("POST /api/v1/cutover", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.SourceCutover
		if e := decode(w, r, &in); e != nil {
			return nil, e
		}
		return s.Cutover(r.Context(), p, in)
	}))
}
