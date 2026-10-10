package articleaudit

import (
	"context"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/logtest"
)

func TestFlowLogsFollowVocabulary(t *testing.T) {
	var recorder logtest.Recorder
	root := t.TempDir()
	articles := &fakeArticles{article: testArticle()}
	blog := &fakeBlog{post: testPost(), found: Post{ID: 22314}}

	broken := flowWithTemplate(t, root, "{{.NoSuchField}}", articles, blog,
		&fakeChats{chat: &fakeChat{answers: []string{fullAnswer}}})
	broken.logger = recorder.Logger()
	if err := broken.Run(context.Background(), "2"); err == nil {
		t.Fatal("Run: ожидался отказ шаблона отчёта")
	}
	retry := flowWithTemplate(t, root, auditTemplate, articles, blog, &fakeChats{chat: &fakeChat{}})
	retry.logger = recorder.Logger()
	if err := retry.Run(context.Background(), "2"); err != nil {
		t.Fatalf("повтор: %v", err)
	}

	logtest.AssertVocabulary(t, recorder.Records(), "fetch", StageAudit)
	for _, record := range recorder.Records() {
		if record.Msg == "ответ модели взят из прошлого прогона" && record.Attr["path"] == "" {
			t.Errorf("record %q: no path", record.Msg)
		}
	}
}
