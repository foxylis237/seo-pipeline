package pprof2

import (
	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/demo"
)

// demoPromptData отдаёт сборщику DEMO те же данные промптов, что уходят в модель в бою.
//
// Своя реализация нужна потому, что основной промпт задачи просит преподавателя: общий набор
// полей DEMO даёт на нём ошибку рендера, и папка выходила без промпта и без текста страницы —
// уже после оплаченной стадии structure.
//
// Второго набора полей здесь нет намеренно: обе функции — те же, что зовёт поток. Разойдись
// они, промпт для ручного чата молча отличался бы от боевого.
type demoPromptData struct{}

func (demoPromptData) StructureData(input article.GenerationInput) any {
	return structureData(input)
}

func (demoPromptData) ArticleData(input article.GenerationInput, structure string) any {
	return articleData(input, structure)
}

// DemoPromptData отдаёт сборщики данных промптов задачи.
//
// Метод потока, а не имя задачи в новом switch: поля промптов знает тот же пакет, что и сами
// промпты, а задача без своих полей метод просто не объявляет — DEMO тогда собирает общий
// набор.
func (f *Flow) DemoPromptData() demo.PromptData { return demoPromptData{} }
