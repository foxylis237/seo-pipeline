package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"text/template"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/articleaudit"
	"github.com/foxylis237/seo-pipeline/internal/tasks"
	"github.com/foxylis237/seo-pipeline/internal/tasks/pprofaudit1"
	"github.com/foxylis237/seo-pipeline/internal/tasks/pprofaudit2"
)

// articleAuditProfiles — задачи аудита из реестра. Список берётся признаком, а не именами:
// третья пачка аудита обязана попасть под эти проверки, ничего здесь не меняя.
func articleAuditProfiles() []tasks.Profile {
	var profiles []tasks.Profile
	for _, profile := range taskRegistry() {
		if profile.ArticleAudit != nil {
			profiles = append(profiles, profile)
		}
	}
	return profiles
}

// Обе задачи аудита видны реестру в обеих формах имени: человек пишет дефисную, логи и схема
// PostgreSQL используют подчёркнутую.
func TestRegistryResolvesArticleAuditTasks(t *testing.T) {
	for _, want := range []string{
		pprofaudit1.Name, pprofaudit1.Command,
		pprofaudit2.Name, pprofaudit2.Command,
	} {
		profile, err := lookupTask(want)
		if err != nil {
			t.Fatalf("задача %q не найдена: %v", want, err)
		}
		if profile.ArticleAudit == nil {
			t.Fatalf("задача %q разрешилась в профиль без признака аудита", want)
		}
		// Признаки не совмещаются: аудит и правка — разные потоки, и задача, у которой
		// непусты оба поля, ушла бы на первую же ветку composition root.
		if profile.ArticleFix != nil {
			t.Fatalf("задача %q объявлена и правкой, и аудитом", want)
		}
	}
	if len(articleAuditProfiles()) < 2 {
		t.Fatalf("задач аудита в реестре %d, ожидалось не меньше двух", len(articleAuditProfiles()))
	}
}

// Файлы задачи обязаны лежать на диске: опечатка в пути профиля иначе всплыла бы посреди
// прогона — после того, как страница уже прочитана из живого блога.
func TestArticleAuditTaskFilesExist(t *testing.T) {
	for _, profile := range articleAuditProfiles() {
		paths := []string{
			profile.ArticleAudit.AuditPromptPath,
			profile.TemplatePath,
			profile.LLMConfigPath,
		}
		for _, path := range paths {
			if _, err := os.Stat(filepath.Join("..", "..", filepath.FromSlash(path))); err != nil {
				t.Fatalf("задача %s: файл %q недоступен: %v", profile.Name, path, err)
			}
		}
	}
}

// Незаполненный промпт останавливает прогон до модели: за каждый ответ платим, а метку в
// промпте легко не заметить.
func TestArticleAuditRefusesUnfilledPrompt(t *testing.T) {
	for _, profile := range articleAuditProfiles() {
		path := filepath.Join("..", "..", filepath.FromSlash(profile.ArticleAudit.AuditPromptPath))
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("задача %s: %v", profile.Name, err)
		}
		filled := articleaudit.EnsurePromptFilled(path) == nil
		hasMarker := strings.Contains(string(content), articleaudit.PromptPlaceholder)
		if filled == hasMarker {
			t.Fatalf("задача %s: метка %q в промпте есть=%v, а прогон разрешён=%v",
				profile.Name, articleaudit.PromptPlaceholder, hasMarker, filled)
		}
	}
}

// reset сбрасывает задачу целиком и ID не принимает: у страницы два состояния — проверена или
// нет, переигрывать поодиночке нечего. Отказ приходит до обращения к базе, поэтому
// проверяется без неё.
func TestArticleAuditResetRefusesExternalID(t *testing.T) {
	for _, profile := range articleAuditProfiles() {
		err := runArticleAuditReset(t.Context(), nil, articleAuditDeps{
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

// Список обязательных полей приходит от человека, и до этого он пуст. Пустой список означает
// «проверка заполненности выключена» — законное состояние, а не недоделка. Тест сторожит
// именно это: набор полей нельзя вывести из живой записи и нельзя придумать за человека.
func TestArticleAuditRequiredFieldsComeFromHuman(t *testing.T) {
	for _, profile := range articleAuditProfiles() {
		for _, field := range profile.ArticleAudit.RequiredFields {
			if strings.TrimSpace(field) == "" {
				t.Fatalf("задача %s: в списке обязательных полей пустое имя", profile.Name)
			}
		}
	}
}

// Промпт обязан спрашивать и текст статьи, и блок вопросов: оцениваем мы статью, а вопросы —
// её часть, которой в теле записи нет вовсе. Промпт без {{.FAQ}} проверял бы половину.
func TestArticleAuditPromptAsksForPageAndFields(t *testing.T) {
	for _, profile := range articleAuditProfiles() {
		path := filepath.Join("..", "..", filepath.FromSlash(profile.ArticleAudit.AuditPromptPath))
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("задача %s: %v", profile.Name, err)
		}
		for _, placeholder := range []string{"{{.Title}}", "{{.OriginalHTML}}", "{{.FAQ}}"} {
			if !strings.Contains(string(content), placeholder) {
				t.Fatalf("задача %s: в промпте нет %s", profile.Name, placeholder)
			}
		}
		// Полей записи в промпте быть не должно: их проверяет код, и модель о них не судит.
		if strings.Contains(string(content), "{{.Fields}}") {
			t.Fatalf("задача %s: поля записи ушли бы в модель", profile.Name)
		}
		// Промпт — шаблон, и опечатка в нём («{{.Fields}» вместо «{{.Fields}}») иначе всплыла
		// бы на живом прогоне: NewFlow разбирает его уже после того, как поднят браузер.
		tpl, err := template.New(profile.Name).Option("missingkey=error").Parse(string(content))
		if err != nil {
			t.Fatalf("задача %s: промпт не разбирается как шаблон: %v", profile.Name, err)
		}
		// И рендерится теми полями, которые ему даёт поток, — лишнее имя роняет прогон.
		if err := tpl.Execute(io.Discard, articleaudit.PromptData{
			Title:        "Обучение на логопеда",
			OriginalHTML: "<p>текст</p>",
			FAQ:          "Вопрос: Сколько учиться?\nОтвет: 320 часов.",
		}); err != nil {
			t.Fatalf("задача %s: промпт не рендерится данными потока: %v", profile.Name, err)
		}
	}
}

// Формат ответа в промпте — это контракт с разбором, и разойтись им нельзя.
//
// Тест собирает ответ модели из того самого формата, который просит промпт задачи: берёт из
// раздела «ФОРМАТ ОТВЕТА» строки-заголовки, подписывает под каждой свою метку и прогоняет
// получившееся через код. Каждая метка обязана оказаться в своём разделе отчёта.
//
// Смысл именно в этом: переименовали раздел в промпте — тест краснеет здесь, а не в отчёте
// живого прогона, где потерянный раздел стоил бы оплаченного ответа.
func TestArticleAuditPromptFormatMatchesParser(t *testing.T) {
	for _, profile := range articleAuditProfiles() {
		path := filepath.Join("..", "..", filepath.FromSlash(profile.ArticleAudit.AuditPromptPath))
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("задача %s: %v", profile.Name, err)
		}
		headings := promptSectionHeadings(string(content))
		if len(headings) != 2 {
			t.Fatalf("задача %s: в формате ответа %d заголовков разделов, ожидалось 2: %v",
				profile.Name, len(headings), headings)
		}
		var answer strings.Builder
		answer.WriteString("Итоговая оценка: 14/20\n\n")
		for index, heading := range headings {
			fmt.Fprintf(&answer, "%s\nметка-%d\n\n", heading, index)
		}
		parsed, err := articleaudit.ParseAnswer(answer.String())
		if err != nil {
			t.Fatalf("задача %s: %v", profile.Name, err)
		}
		if !parsed.ScoreFound || parsed.Score != 14 || parsed.ScoreMax != 20 {
			t.Fatalf("задача %s: оценка из шапки промпта не разобралась: %d/%d",
				profile.Name, parsed.Score, parsed.ScoreMax)
		}
		// Каждая метка легла ровно в один раздел, и разделы не перепутались местами.
		sections := []string{articleaudit.SectionCritical, articleaudit.SectionIssues}
		for index, section := range sections {
			want := fmt.Sprintf("метка-%d", index)
			if got := parsed.Section(section); got != want {
				t.Fatalf("задача %s: раздел %q разобран как %q, ожидалось %q (заголовок в промпте: %q)",
					profile.Name, section, got, want, headings[index])
			}
		}
		// Ничего не потерялось по дороге: у ответа строго по формату остатка быть не может.
		if parsed.Unparsed != "" {
			t.Fatalf("задача %s: ответ по формату промпта дал остаток %q", profile.Name, parsed.Unparsed)
		}
	}
}

// promptSectionHeadings возвращает нумерованные заголовки разделов из «ФОРМАТА ОТВЕТА».
//
// Берутся строки вида «1. Название» ниже заголовка формата: именно их промпт велит
// воспроизводить дословно, и именно по ним ищет разделы код.
func promptSectionHeadings(prompt string) []string {
	position := strings.Index(prompt, "ФОРМАТ ОТВЕТА")
	if position < 0 {
		return nil
	}
	var headings []string
	for _, raw := range strings.Split(prompt[position:], "\n") {
		line := strings.TrimSpace(raw)
		if promptHeadingRE.MatchString(line) {
			headings = append(headings, line)
		}
	}
	return headings
}

// promptHeadingRE — «1. Критические ошибки», но не «1. ...» из примера оформления находки.
var promptHeadingRE = regexp.MustCompile(`^[1-9]\. [А-ЯЁ][^.]*$`)

// codeFence — маркер блока кода, которым модель регулярно оборачивает весь ответ.
const codeFence = "```"

// realisticAnswer — ответ в том виде, в каком его присылает веб-интерфейс модели: обёрнутый
// в блок кода, с markdown-разметкой заголовков, с нумерацией из привычного модели порядка и с
// разделами, которых формат не просит. Всё это разбор обязан пережить: за ответ заплачено, а
// лишние разделы не должны подмешаться в список ошибок.
const realisticAnswer = "```markdown\n" + `Количество слов: 1 240
Ключевые запросы: обучение на логопеда, курсы логопеда дистанционно
Итоговая оценка: 13/20

### 4. Критические ошибки (ТОП-3):

Ошибка: на странице нет блока о документе
Почему это проблема: человек не понимает, что получит на руки, и уходит сравнивать
Как исправить: добавить раздел о документе после программы обучения

Ошибка: плашки обещают рабочую профессию, а страница про логопеда
Почему это проблема: читатель перестаёт верить остальному тексту
Как исправить: привести text_spec_1 и text_spec_3 к профессиональной переподготовке

**5. Все найденные ошибки списком**
По содержанию:
— «В современном мире» в первом абзаце
— «высокий спрос» без цифр
По оформлению:
— абзац на семь строк в разделе о программе
По полям записи:
— text_spec_1 — «Обучение рабочей профессии» на странице логопеда

### 6. Рекомендации
Переписать первый абзац так: «Логопеду для работы в школе нужен диплом…»

### 7. Разбор по критериям
* Структура: 3/4 — заголовки на месте, но абзацы длинные
* SEO: 2/4 — ключ повторяется семь раз
` + "```"

// Реальный шаблон отчёта задачи обязан рендериться реальными данными разбора.
//
// Тест держит третью границу этой цепочки: промпт задаёт формат, код его разбирает
// (TestArticleAuditPromptFormatMatchesParser), а шаблон печатает разобранное. Опечатка в
// имени поля шаблона иначе всплыла бы после оплаченного ответа — уже некуда сохранять отчёт.
func TestArticleAuditTemplateRendersParsedAnswer(t *testing.T) {
	parsed, err := articleaudit.ParseAnswer(realisticAnswer)
	if err != nil {
		t.Fatalf("ParseAnswer: %v", err)
	}
	if !parsed.ScoreFound || parsed.Score != 13 {
		t.Fatalf("оценка разобрана как %d/%d (найдена=%v)", parsed.Score, parsed.ScoreMax, parsed.ScoreFound)
	}
	// Обёртка в блок кода снята, а не утекла в разделы отчёта.
	for _, section := range []string{articleaudit.SectionCritical, articleaudit.SectionIssues} {
		if strings.Contains(parsed.Section(section), codeFence) {
			t.Fatalf("маркер блока кода попал в раздел %q: %q", section, parsed.Section(section))
		}
	}

	article := articleaudit.Article{ExternalID: "2", SourceURL: "https://dpoprof.ru/obuchenie-medpersonala/logoped/",
		Slug: "logoped", Topic: "Логопед"}
	post := articleaudit.Post{ID: 22314, Title: "Обучение на логопеда", PostType: "obuch_med",
		Link: "https://dpoprof.ru/obuchenie-medpersonala/logoped/"}
	check := articleaudit.CheckRequired(
		articleaudit.Post{Fields: map[string]string{"prof_name": "Логопед", "docs_title": ""}},
		[]string{"prof_name", "docs_title", "price_now"})

	for _, profile := range articleAuditProfiles() {
		path := filepath.Join("..", "..", filepath.FromSlash(profile.TemplatePath))
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("задача %s: %v", profile.Name, err)
		}
		tpl, err := template.New(profile.Name).Option("missingkey=error").Parse(string(content))
		if err != nil {
			t.Fatalf("задача %s: шаблон не разобран: %v", profile.Name, err)
		}
		var report strings.Builder
		data := articleaudit.BuildReport(article, post, check, parsed,
			[]string{"prof_name", "docs_title", "price_now"})
		if err := tpl.Execute(&report, data); err != nil {
			t.Fatalf("задача %s: шаблон не отрендерился: %v", profile.Name, err)
		}
		text := report.String()
		// Оценка — первой строкой отчёта: по ней страницы сравнивают между собой.
		if !strings.HasPrefix(text, "# 13/20") {
			t.Fatalf("задача %s: отчёт начинается с %q", profile.Name, strings.SplitN(text, "\n", 2)[0])
		}
		// Ссылка на страницу стоит выше находок и отдельным блоком: по отчёту переходят на
		// саму страницу, и адрес должен копироваться одним движением, а не выделяться из
		// строки таблицы.
		head := strings.SplitN(text, "## Критические", 2)[0]
		if !strings.Contains(head, "```text\nhttps://dpoprof.ru/obuchenie-medpersonala/logoped/\n```") {
			t.Fatalf("задача %s: ссылки на страницу нет в шапке отчёта:\n%s", profile.Name, head)
		}
		// Находки тоже лежат блоками: отчёт копируют кусками, и вычищать разметку после
		// вставки человек не должен.
		if strings.Count(text, "```text") != 4 {
			t.Fatalf("задача %s: не все разделы отчёта копируются блоком:\n%s", profile.Name, text)
		}
		// Совет «как исправить» доезжает до отчёта: он часть критической ошибки.
		if !strings.Contains(text, "Как исправить: добавить раздел о документе") {
			t.Fatalf("задача %s: «как исправить» потеряно:\n%s", profile.Name, text)
		}
		// Ищем внутри самого списка ошибок, а не по всему отчёту: имя поля встречается и в
		// критических ошибках, и первое совпадение пришлось бы на них.
		issues := text[strings.Index(text, "## Все найденные ошибки"):]
		issues = issues[:strings.Index(issues, "## Не разобрано")]
		// Находки кода по полям стоят в том же списке и выше находок модели.
		codeFindings := strings.Index(issues, "поле docs_title — обязательное, но не заполнено")
		modelFindings := strings.Index(issues, "text_spec_1")
		if codeFindings < 0 || modelFindings < 0 || codeFindings > modelFindings {
			t.Fatalf("задача %s: список ошибок собран неверно:\n%s", profile.Name, issues)
		}
		// Разделы, которых формат не просит, в список находок не попали — они отложены
		// в «Не разобрано» целиком.
		if strings.Contains(issues, "Разбор по критериям") || strings.Contains(issues, "Переписать первый абзац") {
			t.Fatalf("задача %s: лишний раздел подмешался в список ошибок:\n%s", profile.Name, issues)
		}
		if !strings.Contains(text, "Переписать первый абзац") {
			t.Fatalf("задача %s: лишний раздел потерян вместо «Не разобрано»:\n%s", profile.Name, text)
		}
		t.Logf("отчёт задачи %s:\n%s", profile.Name, text)
	}
}
