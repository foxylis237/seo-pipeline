package diagnostics

import (
	"strings"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/article"
)

// File names of the prepare diagnostics written next to the article artifacts.
const (
	InputFile         = "input.json"
	KeysSOFile        = "keysso.json"
	ArsenkinFile      = "arsenkin.json"
	PrepareReportFile = "prepare-report.json"
)

// InputSnapshot is what prepare started from: the article row and its imported Excel data.
type InputSnapshot struct {
	ArticleID   int64         `json:"article_id"`
	ExternalID  string        `json:"external_id"`
	Title       string        `json:"title"`
	Slug        string        `json:"slug"`
	Status      string        `json:"status"`
	CurrentStep string        `json:"current_step,omitempty"`
	CollectedAt time.Time     `json:"collected_at"`
	Input       article.Input `json:"input"`
}

// NewInputSnapshot records the article state and its imported row.
func NewInputSnapshot(selected article.Article, input article.Input) InputSnapshot {
	snapshot := InputSnapshot{
		ArticleID: selected.ID, ExternalID: selected.ExternalID, Title: selected.Title,
		Slug: selected.Slug, Status: selected.Status, CollectedAt: time.Now(), Input: input,
	}
	if selected.CurrentStep != nil {
		snapshot.CurrentStep = *selected.CurrentStep
	}
	return snapshot
}

// Where the cleaned queries of an article came from.
const (
	// KeywordSourceKeysSO — запросы собраны браузерной автоматизацией Keys.so.
	KeywordSourceKeysSO = "keysso"
	// KeywordSourceManual — запросы заполнены руками в article_research.cleaned_keywords,
	// этап Keys.so пропущен.
	KeywordSourceManual = "manual"
	// KeywordSourceFallback — исходные запросы подобрала модель, потому что Keys.so не нашёл
	// у конкурента ни одного. Очистка при этом всё равно прошла через Keys.so.
	KeywordSourceFallback = "keywords_fallback"
)

// KeysSOSnapshot is everything the Keys.so stage returned for one article.
type KeysSOSnapshot struct {
	ArticleID       int64     `json:"article_id"`
	ExternalID      string    `json:"external_id"`
	Title           string    `json:"title"`
	Keyword         string    `json:"keyword"`
	ReferenceURL    string    `json:"reference_url"`
	Source          string    `json:"source"`
	CollectedCount  int       `json:"collected_count"`
	CleanedCount    int       `json:"cleaned_count"`
	Fingerprint     string    `json:"cleaned_keywords_fingerprint"`
	DurationMS      int64     `json:"duration_ms"`
	CollectedAt     time.Time `json:"collected_at"`
	CleanedKeywords []string  `json:"cleaned_keywords"`
}

// NewKeysSOSnapshot records the collected queries together with the article they belong to
// and the source they came from — Keys.so or a manual fill.
func NewKeysSOSnapshot(trace article.Trace, source string, collectedCount int, cleaned []string, duration time.Duration) KeysSOSnapshot {
	return KeysSOSnapshot{
		ArticleID: trace.ArticleID, ExternalID: trace.ExternalID, Title: trace.Title,
		Keyword: trace.Keyword, ReferenceURL: trace.ReferenceURL, Source: source,
		CollectedCount: collectedCount, CleanedCount: len(cleaned),
		Fingerprint: Fingerprint(joinLines(cleaned)), DurationMS: duration.Milliseconds(),
		CollectedAt: time.Now(), CleanedKeywords: cleaned,
	}
}

// ArsenkinSnapshot is everything the Arsenkin stage returned, plus what it was given.
type ArsenkinSnapshot struct {
	ArticleID                      int64                      `json:"article_id"`
	ExternalID                     string                     `json:"external_id"`
	Title                          string                     `json:"title"`
	Keyword                        string                     `json:"keyword"`
	SubmittedCount                 int                        `json:"submitted_count"`
	SubmittedFingerprint           string                     `json:"submitted_fingerprint"`
	WordstatCount                  int                        `json:"wordstat_count"`
	LSICount                       int                        `json:"lsi_count"`
	CompetitorStructureLength      int                        `json:"competitor_structure_length"`
	CompetitorStructureFingerprint string                     `json:"competitor_structure_fingerprint"`
	LSIFingerprint                 string                     `json:"lsi_fingerprint"`
	DurationMS                     int64                      `json:"duration_ms"`
	CollectedAt                    time.Time                  `json:"collected_at"`
	SubmittedQueries               []string                   `json:"submitted_queries"`
	WordstatKeywords               []article.KeywordFrequency `json:"wordstat_keywords"`
	CopywriterQueries              []string                   `json:"copywriter_queries"`
	LSIWords                       []string                   `json:"lsi_words"`
	CompetitorStructure            string                     `json:"competitor_structure"`
}

// ArsenkinResult carries the Arsenkin payload without this package depending on the
// integration that produced it.
type ArsenkinResult struct {
	WordstatKeywords    []article.KeywordFrequency
	CopywriterQueries   []string
	LSIWords            []string
	CompetitorStructure string
}

// NewArsenkinSnapshot records the Arsenkin result together with the queries it was given.
func NewArsenkinSnapshot(trace article.Trace, submitted []string, result ArsenkinResult, duration time.Duration) ArsenkinSnapshot {
	return ArsenkinSnapshot{
		ArticleID: trace.ArticleID, ExternalID: trace.ExternalID, Title: trace.Title, Keyword: trace.Keyword,
		SubmittedCount: len(submitted), SubmittedFingerprint: Fingerprint(joinLines(submitted)),
		WordstatCount: len(result.WordstatKeywords), LSICount: len(result.LSIWords),
		CompetitorStructureLength:      len([]rune(result.CompetitorStructure)),
		CompetitorStructureFingerprint: Fingerprint(result.CompetitorStructure),
		LSIFingerprint:                 Fingerprint(joinLines(result.LSIWords)),
		DurationMS:                     duration.Milliseconds(), CollectedAt: time.Now(),
		SubmittedQueries: submitted, WordstatKeywords: result.WordstatKeywords,
		CopywriterQueries: result.CopywriterQueries, LSIWords: result.LSIWords,
		CompetitorStructure: result.CompetitorStructure,
	}
}

func joinLines(values []string) string { return strings.Join(values, "\n") }
