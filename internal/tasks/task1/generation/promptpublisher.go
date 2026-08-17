package generation

// PromptPublisher выгружает готовый промпт статьи во внешнее хранилище.
//
// Интерфейс объявлен здесь, у потребителя, и держится узким намеренно: пайплайну нельзя знать
// ни про Google, ни про Playwright, ни про то, что публикация вообще куда-то ходит по сети.
//
// Контракт метода — **не блокировать**. Публикация обязана возвращать управление сразу, а
// работу вести сама: стадия info идёт следом и ждать чужого браузера не должна. Ошибки
// публикации наружу не возвращаются — они не должны валить генерацию, за которую уже
// заплачено. Их дело — лог и диагностика на стороне реализации.
type PromptPublisher interface {
	PublishArticlePrompt(job ArticlePromptJob)
	// WaitPending дожидается уже выданных заданий.
	//
	// Нужен ровно перед сборкой result.md: она печатает ссылку на документ, а публикация к
	// этому моменту может быть ещё в работе. Ждать здесь безопасно — генерация закончена, и
	// задерживать больше нечего. Контракт «не блокировать» относится к PublishArticlePrompt.
	WaitPending()
}

// ArticlePromptJob — то, что публикация получает от пайплайна.
//
// Prompt передаётся текстом, а не путём к файлу: это ровно та строка, которую роутер отправил
// модели и writer записал в prompts/article_prompt.txt. Чтение файла заново открыло бы окно,
// в котором опубликовать можно не то, что ушло в модель.
type ArticlePromptJob struct {
	ArticleID  int64
	ExternalID string
	Title      string
	Prompt     string
	// PromptPath — путь артефакта относительно OUTPUT_DIR. Нужен только логу и отчёту.
	PromptPath string
}

// noopPromptPublisher — поведение по умолчанию. Пайплайн без подключённой публикации работает
// как раньше, поэтому dry-run и тесты ничего не знают о Google.
type noopPromptPublisher struct{}

func (noopPromptPublisher) PublishArticlePrompt(ArticlePromptJob) {}
func (noopPromptPublisher) WaitPending()                          {}

// SetPromptPublisher подключает публикацию промпта.
//
// Сеттер, а не параметр конструктора: публикация опциональна — в dry-run её нет намеренно,
// потому что этот режим не ходит во внешние сервисы, — и обязательный параметр заставил бы
// передавать nil во всех местах, где её быть не должно.
func (p *Pipeline) SetPromptPublisher(publisher PromptPublisher) {
	if publisher == nil {
		publisher = noopPromptPublisher{}
	}
	p.promptPublisher = publisher
}

// publishArticlePrompt отдаёт промпт публикации, если она подключена.
func (p *Pipeline) publishArticlePrompt(job ArticlePromptJob) {
	if p.promptPublisher == nil {
		return
	}
	p.promptPublisher.PublishArticlePrompt(job)
}

// waitPromptPublished дожидается публикации перед сборкой result.md.
func (p *Pipeline) waitPromptPublished() {
	if p.promptPublisher == nil {
		return
	}
	p.promptPublisher.WaitPending()
}
