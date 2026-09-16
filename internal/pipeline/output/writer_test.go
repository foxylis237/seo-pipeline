package output

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriterSaveArticle(t *testing.T) {
	root := t.TempDir()
	writer := NewWriter(root)

	paths, err := writer.SaveStructure("42", "тестовая-статья", "Промпт структуры", "Итоговая структура")
	if err != nil {
		t.Fatal(err)
	}
	paths, err = writer.SaveArticle("42", "тестовая-статья", "Промпт на русском", "Текст статьи", "gemini-test")
	if err != nil {
		t.Fatal(err)
	}
	if paths.StructurePromptPath != "42-тестовая-статья/prompts/structure_prompt.txt" {
		t.Fatalf("StructurePromptPath = %q", paths.StructurePromptPath)
	}
	if paths.StructurePath != "42-тестовая-статья/generated/structure.txt" {
		t.Fatalf("StructurePath = %q", paths.StructurePath)
	}
	if paths.ArticlePromptPath != "42-тестовая-статья/prompts/article_prompt.txt" {
		t.Fatalf("ArticlePromptPath = %q", paths.ArticlePromptPath)
	}
	if paths.ArticlePath != "42-тестовая-статья/generated/article.txt" {
		t.Fatalf("ArticlePath = %q", paths.ArticlePath)
	}
	if paths.GenerationInfoPath != "42-тестовая-статья/generated/generation_context.json" {
		t.Fatalf("GenerationInfoPath = %q", paths.GenerationInfoPath)
	}
	assertFileText(t, filepath.Join(root, filepath.FromSlash(paths.StructurePromptPath)), "Промпт структуры")
	assertFileText(t, filepath.Join(root, filepath.FromSlash(paths.StructurePath)), "Итоговая структура")
	assertFileText(t, filepath.Join(root, filepath.FromSlash(paths.ArticlePromptPath)), "Промпт на русском")
	assertFileText(t, filepath.Join(root, filepath.FromSlash(paths.ArticlePath)), "Текст статьи")
	contextData, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(paths.GenerationInfoPath)))
	if err != nil || !strings.Contains(string(contextData), `"model": "gemini-test"`) {
		t.Fatalf("generation context = %q, %v", contextData, err)
	}
	paths, err = writer.SaveReview("42", "тестовая-статья", "Промпт ревью", "Результат ревью")
	if err != nil {
		t.Fatal(err)
	}
	paths, err = writer.SaveArticleInfo("42", "тестовая-статья", "Промпт info", "Название, метки, TL;DR и FAQ")
	if err != nil {
		t.Fatal(err)
	}
	paths, err = writer.SaveFixedArticle("42", "тестовая-статья", "Промпт исправления", "Исправленная статья")
	if err != nil {
		t.Fatal(err)
	}
	paths, err = writer.SaveHTML("42", "тестовая-статья", "Промпт HTML", "<h1>Тема</h1>")
	if err != nil {
		t.Fatal(err)
	}
	assertFileText(t, filepath.Join(root, filepath.FromSlash(paths.ReviewPath)), "Результат ревью")
	assertFileText(t, filepath.Join(root, filepath.FromSlash(paths.ArticleInfoPromptPath)), "Промпт info")
	assertFileText(t, filepath.Join(root, filepath.FromSlash(paths.ArticleInfoPath)), "Название, метки, TL;DR и FAQ")
	assertFileText(t, filepath.Join(root, filepath.FromSlash(paths.FixedArticlePath)), "Исправленная статья")
	assertFileText(t, filepath.Join(root, filepath.FromSlash(paths.HTMLPath)), "<h1>Тема</h1>")
	readArticle, err := writer.Read(paths.ArticlePath)
	if err != nil || readArticle != "Текст статьи" {
		t.Fatalf("Read() = %q, %v", readArticle, err)
	}
	if _, err := writer.Read("../secret"); err == nil {
		t.Fatal("Read() accepted a path outside output root")
	}

	if _, err := writer.SaveStructure("42", "тестовая-статья", "Новый промпт структуры", "Новая структура"); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.SaveArticle("42", "тестовая-статья", "Другой промпт", "Другой текст", "fake"); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.SaveReview("42", "тестовая-статья", "Новый промпт ревью", "Новое ревью"); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.SaveArticleInfo("42", "тестовая-статья", "Новый info prompt", "Новая информация"); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.SaveFixedArticle("42", "тестовая-статья", "Новый fix prompt", "Новая исправленная статья"); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.SaveHTML("42", "тестовая-статья", "Новый HTML prompt", "<h2>Новый HTML</h2>"); err != nil {
		t.Fatal(err)
	}
	assertFileText(t, filepath.Join(root, filepath.FromSlash(paths.StructurePath)), "Новая структура")
	assertFileText(t, filepath.Join(root, filepath.FromSlash(paths.ArticlePath)), "Другой текст")
	assertFileText(t, filepath.Join(root, filepath.FromSlash(paths.ReviewPath)), "Новое ревью")
	assertFileText(t, filepath.Join(root, filepath.FromSlash(paths.ArticleInfoPath)), "Новая информация")
	assertFileText(t, filepath.Join(root, filepath.FromSlash(paths.FixedArticlePath)), "Новая исправленная статья")
	assertFileText(t, filepath.Join(root, filepath.FromSlash(paths.HTMLPath)), "<h2>Новый HTML</h2>")
}

func TestPendingArtifactKeepsPublishedFilesOnPersistenceError(t *testing.T) {
	root := t.TempDir()
	writer := NewWriter(root)
	paths, err := writer.SaveStructure("42", "stage", "старый prompt", "старая structure")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := writer.StageStructure("42", "stage", "новый prompt", "новая structure")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending.files) != 2 {
		t.Fatalf("staged files = %d, want 2", len(pending.files))
	}
	for _, file := range pending.files {
		if filepath.Dir(file.temporaryPath) != filepath.Dir(file.finalPath) {
			t.Fatalf("temporary file %q is not in final directory %q", file.temporaryPath, filepath.Dir(file.finalPath))
		}
	}
	databaseErr := errors.New("database rejected stage")
	if err := Commit(func() error { return databaseErr }, pending); err != databaseErr {
		t.Fatalf("Commit() error = %v, want database error", err)
	}
	assertFileText(t, filepath.Join(root, filepath.FromSlash(paths.StructurePromptPath)), "старый prompt")
	assertFileText(t, filepath.Join(root, filepath.FromSlash(paths.StructurePath)), "старая structure")
	assertNoStagingFiles(t, root)
}

func TestPendingArtifactRestoresAllFilesOnPartialRenameError(t *testing.T) {
	root := t.TempDir()
	writer := NewWriter(root)
	paths, err := writer.SaveStructure("42", "stage", "старый prompt", "старая structure")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := writer.StageStructure("42", "stage", "новый prompt", "новая structure")
	if err != nil {
		t.Fatal(err)
	}
	renameCalls := 0
	pending.rename = func(oldPath, newPath string) error {
		renameCalls++
		if renameCalls == 2 {
			return errors.New("forced rename failure")
		}
		return os.Rename(oldPath, newPath)
	}
	persistCalled := false
	if err := Commit(func() error { persistCalled = true; return nil }, pending); err == nil {
		t.Fatal("Commit() error = nil")
	}
	if persistCalled {
		t.Fatal("database callback was called after rename failure")
	}
	assertFileText(t, filepath.Join(root, filepath.FromSlash(paths.StructurePromptPath)), "старый prompt")
	assertFileText(t, filepath.Join(root, filepath.FromSlash(paths.StructurePath)), "старая structure")
	assertNoStagingFiles(t, root)
}

func TestPendingArtifactPartialWriteDoesNotDamagePublishedFile(t *testing.T) {
	root := t.TempDir()
	writer := NewWriter(root)
	paths, err := writer.SaveResult("42", "stage", "old complete result")
	if err != nil {
		t.Fatal(err)
	}
	writer.write = func(file *os.File, data []byte) (int, error) {
		written, _ := file.Write(data[:len(data)/2])
		return written, errors.New("forced partial write")
	}
	if _, err := writer.StageResult("42", "stage", "new incomplete result"); err == nil {
		t.Fatal("StageResult() error = nil")
	}
	assertFileText(t, filepath.Join(root, filepath.FromSlash(paths.ResultPath)), "old complete result")
	assertNoStagingFiles(t, root)
}

func TestPendingArtifactSuccessfulRetryReplacesWholeContent(t *testing.T) {
	root := t.TempDir()
	writer := NewWriter(root)
	paths, err := writer.SaveHTML("42", "stage", "old prompt", "<p>old trailing content</p>")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := writer.StageHTML("42", "stage", "new prompt", "<p>new</p>")
	if err != nil {
		t.Fatal(err)
	}
	if err := Commit(func() error { return nil }, pending); err != nil {
		t.Fatal(err)
	}
	assertFileText(t, filepath.Join(root, filepath.FromSlash(paths.HTMLPromptPath)), "new prompt")
	assertFileText(t, filepath.Join(root, filepath.FromSlash(paths.HTMLPath)), "<p>new</p>")
	assertNoStagingFiles(t, root)
}

func TestWriterRejectsUnsafePathParts(t *testing.T) {
	writer := NewWriter(t.TempDir())
	for _, testCase := range []struct{ externalID, slug string }{
		{"../42", "slug"},
		{"42", "../slug"},
		{"42", `folder\slug`},
		{"", "slug"},
	} {
		if _, err := writer.SaveArticle(testCase.externalID, testCase.slug, "prompt", "article", "fake"); err == nil {
			t.Fatalf("SaveArticle(%q, %q) error = nil", testCase.externalID, testCase.slug)
		}
	}
}

func assertFileText(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("file %s = %q, want %q", path, got, want)
	}
}

func assertNoStagingFiles(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(entry.Name(), ".tmp-") || strings.HasPrefix(entry.Name(), ".backup-") {
			t.Fatalf("staging file remains after operation: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// StageFiles публикует произвольные артефакты одной транзакцией с записью в базу: у задач со
// своей раскладкой слотов ArticlePaths нет, а атомарность нужна та же.
func TestStageFilesPublishesArbitraryLayout(t *testing.T) {
	root := t.TempDir()
	writer := NewWriter(root)
	pending, err := writer.StageFiles(
		File{Path: "12-logoped/original/article.html", Content: []byte("<p>было</p>")},
		File{Path: "12-logoped/result.md", Content: []byte("# отчёт")},
	)
	if err != nil {
		t.Fatalf("StageFiles: %v", err)
	}
	// До Commit на диске лежат только временные файлы: оборванный прогон не оставляет
	// половину отчёта под именем готового.
	if _, err := os.Stat(filepath.Join(root, "12-logoped", "result.md")); !os.IsNotExist(err) {
		t.Fatalf("артефакт опубликован до Commit: %v", err)
	}
	var persisted bool
	if err := Commit(func() error { persisted = true; return nil }, pending); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if !persisted {
		t.Fatal("persist не вызван")
	}
	assertFileText(t, filepath.Join(root, "12-logoped", "original", "article.html"), "<p>было</p>")
	assertFileText(t, filepath.Join(root, "12-logoped", "result.md"), "# отчёт")
	assertNoStagingFiles(t, root)
}

// Ошибка записи в базу откатывает файлы: путь в базе, указывающий на отчёт, которого нет, —
// хуже отсутствия отчёта.
func TestStageFilesRollsBackWhenPersistFails(t *testing.T) {
	root := t.TempDir()
	writer := NewWriter(root)
	pending, err := writer.StageFiles(File{Path: "3-sanitar/result.md", Content: []byte("новый отчёт")})
	if err != nil {
		t.Fatalf("StageFiles: %v", err)
	}
	if err := Commit(func() error { return errors.New("база недоступна") }, pending); err == nil {
		t.Fatal("Commit скрыл ошибку записи в базу")
	}
	if _, err := os.Stat(filepath.Join(root, "3-sanitar", "result.md")); !os.IsNotExist(err) {
		t.Fatalf("файл остался опубликованным после отката: %v", err)
	}
	assertNoStagingFiles(t, root)
}

// Путь, уводящий за корень артефактов, отбивается до записи.
func TestStageFilesRejectsEscapingPath(t *testing.T) {
	writer := NewWriter(t.TempDir())
	for _, path := range []string{"", "../чужое.md", "/etc/passwd"} {
		if _, err := writer.StageFiles(File{Path: path, Content: []byte("x")}); err == nil {
			t.Fatalf("путь %q принят", path)
		}
	}
}
