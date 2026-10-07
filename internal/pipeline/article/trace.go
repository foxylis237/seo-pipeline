package article

// Trace is the identity of one article read straight from PostgreSQL, logged around external
// steps so a mismatch between the article and the data it receives shows up in the logs.
type Trace struct {
	ArticleID    int64
	ExternalID   string
	Title        string
	Keyword      string
	ReferenceURL string
}
