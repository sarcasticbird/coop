package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"
)

type nativeScope struct {
	SHA              string   `json:"sha256"`
	SessionIDs       []string `json:"sessionIDs"`
	MessageID        string   `json:"messageID"`
	HasDurableClaims bool     `json:"hasDurableClaims"`
}

type nativeInterruptionReceipt struct {
	ID             string `json:"id"`
	SessionID      string `json:"session_id"`
	ExpectedSHA256 string `json:"expected_sha256"`
	TimeCreated    int64  `json:"time_created"`
	Snapshot       string `json:"snapshot"`
	Disposition    string `json:"disposition"`
	Effects        string `json:"effects"`
}

func receiptScope(r nativeInterruptionReceipt, j Job, id, sha string) ([]string, error) {
	if r.ID != id || r.SessionID != j.SessionID || r.Disposition != "interrupted" || r.Effects != "unknown" || r.TimeCreated <= 0 || !hexSHA.MatchString(r.ExpectedSHA256) || sha != "" && r.ExpectedSHA256 != sha || BodyHash(r.Snapshot) != r.ExpectedSHA256 {
		return nil, &OpenCodeError{Category: "invalid_interruption_receipt"}
	}
	var snapshot struct {
		Sessions []struct {
			ID        string `json:"id"`
			Directory string `json:"directory"`
			ParentID  string `json:"parent_id"`
		}
	}
	if json.Unmarshal([]byte(r.Snapshot), &snapshot) != nil || len(snapshot.Sessions) == 0 || len(snapshot.Sessions) > 1000 {
		return nil, &OpenCodeError{Category: "invalid_interruption_scope"}
	}
	parents := map[string]string{}
	var ids []string
	for _, session := range snapshot.Sessions {
		if !validSession(session.ID) || session.Directory != j.Checkout {
			return nil, &OpenCodeError{Category: "foreign_interruption_scope"}
		}
		if _, duplicate := parents[session.ID]; duplicate {
			return nil, &OpenCodeError{Category: "foreign_interruption_scope"}
		}
		parents[session.ID] = session.ParentID
		ids = append(ids, session.ID)
	}
	if parent, exists := parents[j.SessionID]; !exists || parent != "" {
		return nil, &OpenCodeError{Category: "foreign_interruption_scope"}
	}
	for id := range parents {
		ancestor := id
		for steps := 0; ancestor != j.SessionID; steps++ {
			parent, exists := parents[ancestor]
			if !exists || parent == "" || steps >= len(parents) {
				return nil, &OpenCodeError{Category: "foreign_interruption_scope"}
			}
			ancestor = parent
		}
	}
	slices.Sort(ids)
	return ids, nil
}

func (c *OpenCodeClient) idleNativeScope(ctx context.Context, j Job, ids []string) error {
	var statuses map[string]struct{ Type string }
	if err := c.request(ctx, "GET", "/session/status", j.Checkout, nil, &statuses); err != nil {
		return err
	}
	for _, id := range ids {
		if state := statuses[id].Type; state != "" && state != "idle" {
			return &OpenCodeError{Category: "recovery_scope_busy"}
		}
	}
	return nil
}

// FenceNativeScope only records interruption. It never calls prompt, continue,
// abort, a tool, or GitHub. An old receipt is NOT proof of the missing turn's
// result. When the native server refuses a new exact-scope seal because that
// scope is already dispositioned, require its live refusal, unchanged idle scope
// and matching immutable earlier receipt. The resulting operator disposition
// still explicitly retains unknown outcome/effects.
func (c *OpenCodeClient) FenceNativeScope(ctx context.Context, j Job, requestID, scopeSHA, priorReceiptID string) (NativeFenceProof, error) {
	var proof NativeFenceProof
	if !validSession(j.SessionID) || !validMessage(j.MessageID) || !nativeRecoveryID.MatchString(requestID) || !nativeRecoveryID.MatchString(priorReceiptID) || requestID == priorReceiptID || !hexSHA.MatchString(scopeSHA) || !cleanPrivatePath(j.Checkout) {
		return proof, &OpenCodeError{Category: "invalid_abandonment_intent"}
	}
	if err := c.CheckIdle(ctx, j); err != nil {
		return proof, err
	}
	var message messageEnvelope
	err := c.requestBounded(ctx, "GET", "/session/"+j.SessionID+"/message/"+j.MessageID, j.Checkout, nil, &message, maxNativeMessagePage)
	var missing *OpenCodeError
	if !errors.As(err, &missing) || missing.Status != http.StatusNotFound {
		return proof, &OpenCodeError{Category: "native_message_not_missing"}
	}
	root := "/session/" + j.SessionID + "/recovery"
	var receipt nativeInterruptionReceipt
	// Observe an acknowledged fence first on retry; never repeat a successful
	// seal just because saving the local cancellation was interrupted.
	err = c.requestBounded(ctx, "GET", root+"/"+requestID, j.Checkout, nil, &receipt, maxNativeMessagePage)
	if err == nil {
		ids, err := receiptScope(receipt, j, requestID, scopeSHA)
		if err != nil || j.SubmittedAt.IsZero() || receipt.TimeCreated < j.SubmittedAt.UnixMilli() {
			return proof, &OpenCodeError{Category: "invalid_interruption_receipt"}
		}
		var current nativeScope
		if err := c.request(ctx, "GET", root, j.Checkout, nil, &current); err != nil {
			return proof, err
		}
		currentIDs := slices.Clone(current.SessionIDs)
		slices.Sort(currentIDs)
		if current.HasDurableClaims || !slices.Equal(currentIDs, ids) {
			return proof, &OpenCodeError{Category: "native_scope_changed"}
		}
		if err := c.idleNativeScope(ctx, j, ids); err != nil {
			return proof, err
		}
		return interruptionProof(receipt, j, scopeSHA, "interrupted"), nil
	}
	var absent *OpenCodeError
	if !errors.As(err, &absent) || absent.Status != http.StatusNotFound && (absent.Status != http.StatusConflict || absent.missingReceiptSession != j.SessionID) {
		return proof, err
	}
	var scope nativeScope
	if err := c.request(ctx, "GET", root, j.Checkout, nil, &scope); err != nil {
		return proof, err
	}
	ids := slices.Clone(scope.SessionIDs)
	slices.Sort(ids)
	if scope.SHA != scopeSHA || scope.HasDurableClaims || !slices.Contains(ids, j.SessionID) || !validMessage(scope.MessageID) || len(ids) == 0 || len(ids) > 1000 {
		return proof, &OpenCodeError{Category: "native_scope_changed"}
	}
	if err := c.idleNativeScope(ctx, j, ids); err != nil {
		return proof, err
	}
	var prior nativeInterruptionReceipt
	if err := c.requestBounded(ctx, "GET", root+"/"+priorReceiptID, j.Checkout, nil, &prior, maxNativeMessagePage); err != nil {
		return proof, err
	}
	priorIDs, err := receiptScope(prior, j, priorReceiptID, "")
	if err != nil || !slices.Equal(priorIDs, ids) {
		return proof, &OpenCodeError{Category: "native_scope_changed"}
	}
	body := struct {
		ID             string `json:"id"`
		ExpectedSHA256 string `json:"expectedSHA256"`
	}{requestID, scopeSHA}
	err = c.requestBounded(ctx, "POST", root, j.Checkout, body, &receipt, maxNativeMessagePage)
	if err == nil {
		sealed, err := receiptScope(receipt, j, requestID, scopeSHA)
		if err != nil || !slices.Equal(sealed, ids) || j.SubmittedAt.IsZero() || receipt.TimeCreated < j.SubmittedAt.UnixMilli() {
			return proof, &OpenCodeError{Category: "invalid_interruption_receipt"}
		}
		if err := c.idleNativeScope(ctx, j, ids); err != nil {
			return proof, err
		}
		return interruptionProof(receipt, j, scopeSHA, "interrupted"), nil
	}
	var sealed *OpenCodeError
	if !errors.As(err, &sealed) || sealed.Status != http.StatusConflict || sealed.sealedSession != j.SessionID {
		return proof, err
	}
	// A generic conflict, old snapshot, or idle session alone is insufficient.
	var current nativeScope
	if err := c.request(ctx, "GET", root, j.Checkout, nil, &current); err != nil {
		return proof, err
	}
	currentIDs := slices.Clone(current.SessionIDs)
	slices.Sort(currentIDs)
	if current.SHA != scope.SHA || current.HasDurableClaims || current.MessageID != scope.MessageID || !slices.Equal(currentIDs, ids) {
		return proof, &OpenCodeError{Category: "native_scope_changed"}
	}
	if err := c.idleNativeScope(ctx, j, ids); err != nil {
		return proof, err
	}
	return interruptionProof(prior, j, scopeSHA, "already_dispositioned"), nil
}

func interruptionProof(receipt nativeInterruptionReceipt, j Job, scopeSHA, disposition string) NativeFenceProof {
	raw, _ := json.Marshal(receipt)
	return NativeFenceProof{ReceiptID: receipt.ID, ReceiptSHA: BodyHash(string(raw)), ScopeSHA: scopeSHA, SessionID: j.SessionID, Disposition: disposition, VerifiedAt: time.Now().UTC()}
}
