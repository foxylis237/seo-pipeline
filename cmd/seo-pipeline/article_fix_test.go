package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/articlefix"
	"github.com/foxylis237/seo-pipeline/internal/tasks"
	"github.com/foxylis237/seo-pipeline/internal/tasks/pproffix1"
	"github.com/foxylis237/seo-pipeline/internal/tasks/pproffix2"
	"github.com/foxylis237/seo-pipeline/internal/tasks/pproffix3"
	"github.com/foxylis237/seo-pipeline/internal/tasks/pproffix4"
	"github.com/foxylis237/seo-pipeline/internal/tasks/pproffix5"
)

// articleFixProfiles — задачи правки из реестра. Список берётся признаком, а не именами:
// новая задача правки обязана попасть под эти проверки, ничего здесь не меняя.
func articleFixProfiles() []tasks.Profile {
	var profiles []tasks.Profile
	for _, profile := range taskRegistry() {
		if profile.ArticleFix != nil {
			profiles = append(profiles, profile)
		}
	}
	return profiles
}

// Обе задачи правки видны реестру в обеих формах имени: человек пишет дефисную, логи и схема
// PostgreSQL используют подчёркнутую.
func TestRegistryResolvesArticleFixTasks(t *testing.T) {
	for _, want := range []string{
		pproffix1.Name, pproffix1.Command,
		pproffix2.Name, pproffix2.Command,
		pproffix3.Name, pproffix3.Command,
		pproffix4.Name, pproffix4.Command,
		pproffix5.Name, pproffix5.Command,
	} {
		profile, err := lookupTask(want)
		if err != nil {
			t.Fatalf("задача %q не найдена: %v", want, err)
		}
		if profile.ArticleFix == nil {
			t.Fatalf("задача %q разрешилась в профиль без признака правки", want)
		}
	}
	if len(articleFixProfiles()) < 3 {
		t.Fatalf("задач правки в реестре %d, ожидалось не меньше трёх", len(articleFixProfiles()))
	}
}

// Файлы задачи обязаны лежать на диске: опечатка в пути профиля иначе всплыла бы посреди
// прогона — после того, как статья уже прочитана из живого блога.
func TestArticleFixTaskFilesExist(t *testing.T) {
	for _, profile := range articleFixProfiles() {
		paths := []string{
			profile.ArticleFix.RewritePromptPath,
			profile.TemplatePath,
			profile.LLMConfigPath,
		}
		// Правило переименования — единственный необязательный файл: пустой путь означает
		// «заголовок не трогаем», и файла у такой задачи нет по замыслу. Проверять пустую
		// строку нельзя — filepath.Join отдал бы корень проекта, и os.Stat молча прошёл бы.
		if profile.ArticleFix.TitleRulePath != "" {
			paths = append(paths, profile.ArticleFix.TitleRulePath)
		}
		for _, path := range paths {
			if _, err := os.Stat(filepath.Join("..", "..", filepath.FromSlash(path))); err != nil {
				t.Fatalf("задача %s: файл %q недоступен: %v", profile.Name, path, err)
			}
		}
	}
}

// Незаполненный промпт останавливает прогон до модели и до блога: правку живых статей
// откатить нечем, а метку в промпте легко не заметить.
func TestArticleFixRefusesUnfilledPrompt(t *testing.T) {
	for _, profile := range articleFixProfiles() {
		path := filepath.Join("..", "..", filepath.FromSlash(profile.ArticleFix.RewritePromptPath))
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("задача %s: %v", profile.Name, err)
		}
		filled := articlefix.EnsurePromptFilled(path) == nil
		hasMarker := strings.Contains(string(content), articlefix.PromptPlaceholder)
		if filled == hasMarker {
			t.Fatalf("задача %s: метка %q в промпте есть=%v, а прогон разрешён=%v",
				profile.Name, articlefix.PromptPlaceholder, hasMarker, filled)
		}
	}
}

// reset сбрасывает задачу целиком и ID не принимает: у статьи два состояния — переписана или
// нет, переигрывать поодиночке нечего. Отказ приходит до обращения к базе, поэтому проверяется
// без неё.
func TestArticleFixResetRefusesExternalID(t *testing.T) {
	for _, profile := range articleFixProfiles() {
		err := runArticleFixReset(t.Context(), nil, articleFixDeps{
			profile: profile,
			command: taskCommand{Profile: profile, Name: "reset", ExternalID: "12"},
			output:  io.Discard,
		})
		if err == nil {
			t.Fatalf("%s: reset с ID вернул успех", profile.Command)
		}
		if !strings.Contains(err.Error(), "ID не принимает") {
			t.Fatalf("%s: отказ не объясняет причину: %v", profile.Command, err)
		}
	}
}

// Правило переименования выбирается по профилю, а не по имени задачи: у задачи без файла
// правил заголовок обязан остаться прежним, а не уронить прогон отсутствующим файлом.
func TestArticleFixTitleRuleFollowsProfile(t *testing.T) {
	for _, profile := range articleFixProfiles() {
		// Пути в профиле относительны корня проекта, а тест идёт из cmd/seo-pipeline.
		local := profile
		fix := *profile.ArticleFix
		if fix.TitleRulePath != "" {
			fix.TitleRulePath = filepath.Join("..", "..", filepath.FromSlash(fix.TitleRulePath))
		}
		local.ArticleFix = &fix

		rule, err := articleFixTitleRule(local)
		if err != nil {
			t.Fatalf("задача %s: правило не собралось: %v", profile.Name, err)
		}
		_, keeps := rule.(articlefix.KeepTitle)
		if keeps != (profile.ArticleFix.TitleRulePath == "") {
			t.Fatalf("задача %s: путь к правилу %q, а заголовок сохраняется=%v",
				profile.Name, profile.ArticleFix.TitleRulePath, keeps)
		}
	}
}

// pprof_fix_3 заголовок не трогает: и название записи, и видимый H1 остаются прежними, а
// правка ограничена текстом. Проверяется на профиле, потому что это его единственное отличие.
func TestArticleFixThreeKeepsTitle(t *testing.T) {
	profile, err := lookupTask(pproffix3.Command)
	if err != nil {
		t.Fatalf("задача %s не найдена: %v", pproffix3.Command, err)
	}
	if profile.ArticleFix.TitleRulePath != "" {
		t.Fatalf("у %s появилось правило переименования %q, хотя заголовок она не меняет",
			profile.Name, profile.ArticleFix.TitleRulePath)
	}
	rule, err := articleFixTitleRule(profile)
	if err != nil {
		t.Fatalf("правило не собралось: %v", err)
	}
	const title = "Врач-хирург - дистанционная переподготовка"
	got, err := rule.Apply(title)
	if err != nil {
		t.Fatalf("правило отвергло заголовок: %v", err)
	}
	if got != title {
		t.Fatalf("заголовок изменился: %q → %q", title, got)
	}
}
