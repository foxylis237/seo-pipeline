package article

// Trace is the identity of one article read straight from PostgreSQL.
//
// It exists only for diagnostics: every external step of the pipeline reads it before and
// after the call, so a mismatch between the article being processed and the data it receives
// becomes visible in the logs instead of silently reaching article_research.
type Trace struct {
	ArticleID    int64
	ExternalID   string
	Title        string
	Keyword      string
	ReferenceURL string
}
