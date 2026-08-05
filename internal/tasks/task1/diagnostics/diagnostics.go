// Package diagnostics contains temporary tracing and mechanical consistency checks used to
// locate cross-article data mix-ups.
//
// Nothing here talks to an LLM and nothing here decides the pipeline flow: helpers either emit
// a log record or return a check result, and the caller decides how to treat it.
package diagnostics

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"unicode"

	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/article"
)

// TraceFields renders one article identity as slog attributes.
func TraceFields(trace article.Trace) []any {
	return []any{
		"article_id", trace.ArticleID,
		"external_id", trace.ExternalID,
		"title", trace.Title,
		"keyword", trace.Keyword,
		"reference_url", trace.ReferenceURL,
	}
}

// LogStep records the article identity around one external step. The phase is "before" or
// "after", the integration names the external system ("keysso", "arsenkin", "save_research").
func LogStep(logger *slog.Logger, integration, phase string, trace article.Trace, extra ...any) {
	if logger == nil {
		return
	}
	fields := []any{"stage", "identity_trace", "integration", integration, "phase", phase}
	fields = append(fields, TraceFields(trace)...)
	logger.Info("article identity trace", append(fields, extra...)...)
}

// TraceMismatch reports the identity of one article changing between two reads.
func TraceMismatch(expected, actual article.Trace) error {
	if expected.ArticleID == actual.ArticleID &&
		expected.ExternalID == actual.ExternalID &&
		expected.Title == actual.Title {
		return nil
	}
	return fmt.Errorf(
		"идентичность статьи изменилась во время обработки: было article_id=%d external_id=%s title=%q, стало article_id=%d external_id=%s title=%q",
		expected.ArticleID, expected.ExternalID, expected.Title,
		actual.ArticleID, actual.ExternalID, actual.Title,
	)
}

// Fingerprint returns a short stable hash of a payload. Two articles logging the same
// fingerprint for competitor structure or keywords received literally the same data.
func Fingerprint(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(sum[:])[:12]
}

// Sample returns at most limit values, for logging without flooding the log.
func Sample(values []string, limit int) []string {
	if len(values) <= limit {
		return append([]string(nil), values...)
	}
	return append([]string(nil), values[:limit]...)
}

const (
	minTokenRunes        = 4
	maxStemRunes         = 6
	minCommonPrefixRunes = 4
	sampleLimit          = 5
)

// KeywordRelevance reports how many collected queries share a word with the article keyword
// or title. It is a coarse smoke test: Keys.so returns queries of the competitor page named
// in reference_url, so they must be about the same subject as the article itself.
type KeywordRelevance struct {
	// Skipped is true when neither keyword nor title produced a usable reference word.
	Skipped bool
	// KeywordBased is true when the article keyword itself produced a reference word. Only
	// then is a zero match reliable enough to block the article: a title alone may legitimately
	// share no word with the queries of the competitor page.
	KeywordBased bool
	// Reference holds the normalized stems the queries were compared against.
	Reference []string
	Checked   int
	Matched   int
	Unmatched []string
}

// Ratio is the share of collected queries related to the article, from 0 to 1.
func (r KeywordRelevance) Ratio() float64 {
	if r.Checked == 0 {
		return 0
	}
	return float64(r.Matched) / float64(r.Checked)
}

// Fields renders the check result as slog attributes.
func (r KeywordRelevance) Fields() []any {
	return []any{
		"check", "keyword_relevance",
		"skipped", r.Skipped,
		"keyword_based", r.KeywordBased,
		"reference_stems", r.Reference,
		"checked_count", r.Checked,
		"matched_count", r.Matched,
		"matched_ratio", r.Ratio(),
		"unmatched_sample", r.Unmatched,
	}
}

// CheckKeywordRelevance compares collected queries with the article keyword and title.
//
// Comparison is intentionally primitive and library-free: words are lowercased, ё is folded
// to е, short words are dropped, and the remaining words are compared by a common prefix.
// That tolerates most Russian declension ("курсы" ~ "курсов", "врач" ~ "врача") without a
// stemmer and is enough to tell "queries of this article" from "queries of another one".
//
// A fleeting vowel still breaks the stem too early to be caught ("окна" ~ "окон"), so the
// check relies on at least one query out of the whole collected list carrying a plain form
// of the keyword. That holds for a real Keys.so list of hundreds of queries; a zero match
// across all of them means the browser showed the results of a different search.
func CheckKeywordRelevance(keyword, title string, queries []string) KeywordRelevance {
	keywordStems := stems(keyword)
	reference := stems(keyword + " " + title)
	result := KeywordRelevance{KeywordBased: len(keywordStems) > 0, Reference: reference, Checked: len(queries)}
	if len(reference) == 0 {
		result.Skipped = true
		return result
	}
	for _, query := range queries {
		if matchesAny(stems(query), reference) {
			result.Matched++
			continue
		}
		if len(result.Unmatched) < sampleLimit {
			result.Unmatched = append(result.Unmatched, query)
		}
	}
	return result
}

// QueryMembership reports how many returned phrases were actually submitted. Arsenkin
// Wordstat answers with the very phrases it was given, so anything else means the page
// showed the result of a different task.
type QueryMembership struct {
	Returned  int
	Matched   int
	Unmatched []string
}

// Ratio is the share of returned phrases that were submitted, from 0 to 1.
func (m QueryMembership) Ratio() float64 {
	if m.Returned == 0 {
		return 0
	}
	return float64(m.Matched) / float64(m.Returned)
}

// Fields renders the check result as slog attributes.
func (m QueryMembership) Fields() []any {
	return []any{
		"check", "query_membership",
		"returned_count", m.Returned,
		"matched_count", m.Matched,
		"matched_ratio", m.Ratio(),
		"unmatched_sample", m.Unmatched,
	}
}

// CheckQueryMembership verifies that returned phrases come from the submitted list.
func CheckQueryMembership(submitted, returned []string) QueryMembership {
	known := make(map[string]struct{}, len(submitted))
	for _, phrase := range submitted {
		if normalized := normalizePhrase(phrase); normalized != "" {
			known[normalized] = struct{}{}
		}
	}
	result := QueryMembership{Returned: len(returned)}
	for _, phrase := range returned {
		if _, found := known[normalizePhrase(phrase)]; found {
			result.Matched++
			continue
		}
		if len(result.Unmatched) < sampleLimit {
			result.Unmatched = append(result.Unmatched, phrase)
		}
	}
	return result
}

func normalizePhrase(value string) string {
	return strings.Join(strings.Fields(fold(value)), " ")
}

func fold(value string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "ё", "е")
}

// stems splits a phrase into comparable word beginnings.
func stems(value string) []string {
	words := strings.FieldsFunc(fold(value), func(symbol rune) bool {
		return !unicode.IsLetter(symbol) && !unicode.IsDigit(symbol)
	})
	result := make([]string, 0, len(words))
	seen := make(map[string]struct{}, len(words))
	for _, word := range words {
		runes := []rune(word)
		if len(runes) < minTokenRunes {
			continue
		}
		if len(runes) > maxStemRunes {
			runes = runes[:maxStemRunes]
		}
		stem := string(runes)
		if _, found := seen[stem]; found {
			continue
		}
		seen[stem] = struct{}{}
		result = append(result, stem)
	}
	return result
}

func matchesAny(candidates, reference []string) bool {
	for _, candidate := range candidates {
		for _, expected := range reference {
			if matches(candidate, expected) {
				return true
			}
		}
	}
	return false
}

// matches reports two word beginnings pointing at the same word.
//
// Requiring one stem to be a prefix of the other is too strict for Russian: declension
// rewrites the tail, and a four-rune word has no room for it — "окна" and "окон" already
// diverge on the fourth rune. So the comparison is a common prefix: four runes for long
// stems and one less when either stem is short. Over-matching is acceptable here, the check
// only has to tell "queries of this article" from "queries of a completely different one".
func matches(left, right string) bool {
	leftRunes, rightRunes := []rune(left), []rune(right)
	common := 0
	for common < len(leftRunes) && common < len(rightRunes) && leftRunes[common] == rightRunes[common] {
		common++
	}
	required := minCommonPrefixRunes
	if len(leftRunes) <= minTokenRunes || len(rightRunes) <= minTokenRunes {
		required = minCommonPrefixRunes - 1
	}
	return common >= required
}
