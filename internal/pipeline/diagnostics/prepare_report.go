package diagnostics

import (
	"time"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
)

// Statuses of one check and of the whole prepare run.
const (
	StatusPassed  = "passed"
	StatusFailed  = "failed"
	StatusSkipped = "skipped"
)

// CheckResult is the outcome of one verification made during prepare.
type CheckResult struct {
	Name    string         `json:"name"`
	Status  string         `json:"status"`
	Details map[string]any `json:"details,omitempty"`
	Message string         `json:"message,omitempty"`
}

// PrepareReport collects everything prepare verified about one article. It is written to
// prepare/prepare-report.json on success and on failure alike: a failed run is exactly when
// the report matters most.
type PrepareReport struct {
	ArticleID    int64         `json:"article_id"`
	ExternalID   string        `json:"external_id"`
	Title        string        `json:"title"`
	Keyword      string        `json:"keyword"`
	ReferenceURL string        `json:"reference_url"`
	Status       string        `json:"status"`
	FailedStage  string        `json:"failed_stage,omitempty"`
	Error        string        `json:"error,omitempty"`
	StartedAt    time.Time     `json:"started_at"`
	FinishedAt   time.Time     `json:"finished_at"`
	DurationMS   int64         `json:"duration_ms"`
	Checks       []CheckResult `json:"checks"`
}

// NewPrepareReport starts a report from the article state known before any external call.
func NewPrepareReport(selected article.Article) *PrepareReport {
	return &PrepareReport{
		ArticleID: selected.ID, ExternalID: selected.ExternalID, Title: selected.Title,
		ReferenceURL: selected.ReferenceURL, StartedAt: time.Now(), Checks: []CheckResult{},
	}
}

// UseTrace replaces the identity with the one re-read from PostgreSQL.
func (r *PrepareReport) UseTrace(trace article.Trace) {
	r.ArticleID = trace.ArticleID
	r.ExternalID = trace.ExternalID
	r.Title = trace.Title
	r.Keyword = trace.Keyword
	if trace.ReferenceURL != "" {
		r.ReferenceURL = trace.ReferenceURL
	}
}

// Add records the outcome of one check.
func (r *PrepareReport) Add(name, status, message string, details map[string]any) {
	r.Checks = append(r.Checks, CheckResult{Name: name, Status: status, Details: details, Message: message})
}

// Pass records a successful check.
func (r *PrepareReport) Pass(name string, details map[string]any) {
	r.Add(name, StatusPassed, "", details)
}

// Fail records a check that stopped the article.
func (r *PrepareReport) Fail(name, message string, details map[string]any) {
	r.Add(name, StatusFailed, message, details)
}

// Skip records a check that could not be made.
func (r *PrepareReport) Skip(name, message string, details map[string]any) {
	r.Add(name, StatusSkipped, message, details)
}

// AddKeywordRelevance records the Keys.so relevance check with its numbers.
func (r *PrepareReport) AddKeywordRelevance(relevance KeywordRelevance, blocked bool) {
	details := map[string]any{
		"keyword_based":   relevance.KeywordBased,
		"reference_stems": relevance.Reference,
		"checked_count":   relevance.Checked,
		"matched_count":   relevance.Matched,
		"matched_ratio":   relevance.Ratio(),
	}
	if len(relevance.Unmatched) > 0 {
		details["unmatched_sample"] = relevance.Unmatched
	}
	switch {
	case blocked:
		r.Fail("keyword_relevance", "ни один запрос Keys.so не связан с ключевым словом статьи", details)
	case relevance.Skipped:
		r.Skip("keyword_relevance", "у статьи нет ключевого слова и названия, пригодных для сравнения", details)
	default:
		r.Pass("keyword_relevance", details)
	}
}

// AddQueryMembership records the Wordstat membership check with its numbers.
func (r *PrepareReport) AddQueryMembership(membership QueryMembership, blocked bool) {
	details := map[string]any{
		"returned_count": membership.Returned,
		"matched_count":  membership.Matched,
		"matched_ratio":  membership.Ratio(),
	}
	if len(membership.Unmatched) > 0 {
		details["unmatched_sample"] = membership.Unmatched
	}
	switch {
	case blocked:
		r.Fail("wordstat_membership", "Wordstat вернул запросы, которые не отправлялись для этой статьи", details)
	case membership.Returned == 0:
		r.Skip("wordstat_membership", "Wordstat не вернул ни одного запроса", details)
	default:
		r.Pass("wordstat_membership", details)
	}
}

// Finish closes the report with the outcome of the whole prepare run.
func (r *PrepareReport) Finish(stage string, err error) {
	r.FinishedAt = time.Now()
	r.DurationMS = r.FinishedAt.Sub(r.StartedAt).Milliseconds()
	if err == nil {
		r.Status = StatusPassed
		return
	}
	r.Status = StatusFailed
	r.FailedStage = stage
	r.Error = err.Error()
}
