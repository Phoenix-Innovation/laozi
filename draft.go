package laozi

import (
	"context"
	"fmt"
	"time"
)

// ============================================================================
// Human validation loop
//
// When a category is created via DSL, it is NOT registered for analysis
// immediately. It becomes a Draft that must be approved by a human first.
// Because Laozi is a plugin, it cannot render UI: instead it produces a Draft
// (fully JSON-serializable, including each expression's compiled SQL and any
// validation errors) that the host app surfaces for review, and the app calls
// ApproveDraft / RejectDraft. Only on approval is the category promoted to
// production (registered so Analyze will use it).
// ============================================================================

// Status is the lifecycle state of a Draft.
type Status string

const (
	StatusDraft    Status = "draft"
	StatusApproved Status = "approved"
	StatusRejected Status = "rejected"
)

// ExpressionReview is the per-expression review payload the host app renders.
type ExpressionReview struct {
	Metric     string   `json:"metric"`
	Expression string   `json:"expression"`
	SQL        string   `json:"sql,omitempty"` // compiled SQL (when valid)
	Valid      bool     `json:"valid"`
	Errors     []string `json:"errors,omitempty"` // syntax/semantic errors from the test parser
}

// Draft is a pending change awaiting human approval.
type Draft struct {
	ID           string             `json:"id"`
	Kind         string             `json:"kind"` // "category"
	Status       Status             `json:"status"`
	CreatedAt    time.Time          `json:"created_at"`
	CreatedBy    string             `json:"created_by,omitempty"` // who proposed
	Category     *Category          `json:"category,omitempty"`
	Expressions  []ExpressionReview `json:"expressions,omitempty"`
	DecidedAt    time.Time          `json:"decided_at,omitempty"` // when approved/rejected
	DecidedBy    string             `json:"decided_by,omitempty"` // who approved/rejected
	RejectReason string             `json:"reject_reason,omitempty"`
}

// Reviewer is an optional hook. If set via WithReviewer, OnDraft is called
// whenever a new draft is created so the host app can surface it for approval.
type Reviewer interface {
	OnDraft(d *Draft)
}

// WithReviewer registers a hook notified when drafts are created.
func WithReviewer(r Reviewer) Option {
	return func(e *Engine) { e.reviewer = r }
}

// approvalRequired reports whether the human gate is active. It honors the
// RequireApproval config default and the per-engine AutoApprove override.
func (e *Engine) approvalRequired() bool {
	return RequireApproval && !e.cfg.AutoApprove
}

// reviewExpressions runs the test parser over every threshold expression and
// returns the per-expression review plus whether all expressions are valid.
func reviewExpressions(cat Category) (reviews []ExpressionReview, allValid bool) {
	allValid = true
	for _, t := range cat.Thresholds {
		if t.Expression == "" {
			continue
		}
		r := ExpressionReview{Metric: t.Metric, Expression: t.Expression}
		if errs := CheckDSL(t.Expression); len(errs) > 0 {
			r.Valid = false
			allValid = false
			for _, e := range errs {
				r.Errors = append(r.Errors, e.Error())
			}
		} else {
			r.Valid = true
			if sql, err := CompileSQL(t.Expression); err == nil {
				r.SQL = sql
			}
		}
		reviews = append(reviews, r)
	}
	return reviews, allValid
}

// ProposeCategory validates any DSL expressions on the category and creates a
// Draft for human review. The category is NOT registered until ApproveDraft is
// called.
//
//   - If any expression has DSL errors, no draft is created and the errors are
//     returned (the host should have called CheckExpression while editing).
//   - If approval is disabled (AutoApprove), the category is registered
//     immediately and the returned draft is already StatusApproved.
func (e *Engine) ProposeCategory(cat Category, actor string) (*Draft, error) {
	reviews, allValid := reviewExpressions(cat)
	if !allValid {
		var msgs []string
		for _, r := range reviews {
			if !r.Valid {
				msgs = append(msgs, fmt.Sprintf("%s: %v", r.Metric, r.Errors))
			}
		}
		return nil, fmt.Errorf("category %q has invalid DSL expressions: %v", cat.ID, msgs)
	}

	e.mu.Lock()
	e.nextDraft++
	id := fmt.Sprintf("draft-%d", e.nextDraft)
	catCopy := cat
	d := &Draft{
		ID:          id,
		Kind:        "category",
		Status:      StatusDraft,
		CreatedAt:   time.Now(),
		CreatedBy:   actor,
		Category:    &catCopy,
		Expressions: reviews,
	}
	e.drafts[id] = d
	auto := !e.approvalRequired()
	if auto {
		d.Status = StatusApproved
		d.DecidedBy = actor
		d.DecidedAt = time.Now()
		e.categories[cat.ID] = cat // promote immediately
	}
	reviewer := e.reviewer
	e.mu.Unlock()

	if err := e.emitAudit(context.Background(), AuditEvent{
		Time: d.CreatedAt, Kind: "draft_proposed", Actor: actor, DraftID: id, CategoryID: cat.ID,
	}); err != nil && e.auditStrict {
		return d, fmt.Errorf("audit write failed: %w", err)
	}
	if auto {
		if err := e.emitAudit(context.Background(), AuditEvent{
			Time: d.DecidedAt, Kind: "draft_approved", Actor: actor, DraftID: id, CategoryID: cat.ID,
			Detail: "auto-approved (approval gate disabled)",
		}); err != nil && e.auditStrict {
			return d, fmt.Errorf("audit write failed: %w", err)
		}
	}
	if reviewer != nil {
		reviewer.OnDraft(d)
	}
	return d, nil
}

// ApproveDraft promotes a draft to production. For a category draft this
// registers the category so Analyze will use it.
func (e *Engine) ApproveDraft(id, actor string) error {
	e.mu.Lock()
	d, ok := e.drafts[id]
	if !ok {
		e.mu.Unlock()
		return fmt.Errorf("draft not found: %s", id)
	}
	if d.Status != StatusDraft {
		e.mu.Unlock()
		return fmt.Errorf("draft %s is %s, not pending", id, d.Status)
	}
	d.Status = StatusApproved
	d.DecidedBy = actor
	d.DecidedAt = time.Now()
	catID := ""
	if d.Kind == "category" && d.Category != nil {
		e.categories[d.Category.ID] = *d.Category
		catID = d.Category.ID
	}
	e.mu.Unlock()

	if err := e.emitAudit(context.Background(), AuditEvent{
		Time: time.Now(), Kind: "draft_approved", Actor: actor, DraftID: id, CategoryID: catID,
	}); err != nil && e.auditStrict {
		return fmt.Errorf("audit write failed: %w", err)
	}
	return nil
}

// RejectDraft marks a draft rejected; it is never promoted.
func (e *Engine) RejectDraft(id, actor, reason string) error {
	e.mu.Lock()
	d, ok := e.drafts[id]
	if !ok {
		e.mu.Unlock()
		return fmt.Errorf("draft not found: %s", id)
	}
	if d.Status != StatusDraft {
		e.mu.Unlock()
		return fmt.Errorf("draft %s is %s, not pending", id, d.Status)
	}
	d.Status = StatusRejected
	d.DecidedBy = actor
	d.DecidedAt = time.Now()
	d.RejectReason = reason
	catID := ""
	if d.Category != nil {
		catID = d.Category.ID
	}
	e.mu.Unlock()

	if err := e.emitAudit(context.Background(), AuditEvent{
		Time: time.Now(), Kind: "draft_rejected", Actor: actor, DraftID: id, CategoryID: catID, Detail: reason,
	}); err != nil && e.auditStrict {
		return fmt.Errorf("audit write failed: %w", err)
	}
	return nil
}

// Draft returns a draft by ID.
func (e *Engine) Draft(id string) (*Draft, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	d, ok := e.drafts[id]
	return d, ok
}

// PendingDrafts returns all drafts still awaiting approval.
func (e *Engine) PendingDrafts() []*Draft {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var out []*Draft
	for _, d := range e.drafts {
		if d.Status == StatusDraft {
			out = append(out, d)
		}
	}
	return out
}

// CheckExpression validates a single DSL expression, returning all errors
// (empty = valid). Host apps call this live as a user edits an expression.
func (e *Engine) CheckExpression(expr string) []DSLError {
	return CheckDSL(expr)
}
