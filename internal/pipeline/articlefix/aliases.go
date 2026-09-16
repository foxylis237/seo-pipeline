package articlefix

import "github.com/foxylis237/seo-pipeline/internal/pipeline/pagebatch"

// Разбор входа и предохранитель прогона переехали в internal/pipeline/pagebatch: ни в том,
// ни в другом нет ни строчки про правку, а задаче аудита нужны ровно они же. Здесь остались
// имена, которыми их зовут поток и composition root, — алиасы, а не обёртки: у обёртки была бы
// своя жизнь, и две реализации одного правила разошлись бы молча.
//
// Второй копии разбора в проекте быть не должно: правила у него нетривиальные — адрес под
// текстом ячейки, README любого расширения, дубль индекса с номером строки, — и расхождение
// обнаружилось бы недостающей статьёй в блоге.
type (
	// Source — одна строка входного файла: индекс статьи и ссылка на неё.
	Source = pagebatch.Source
	// FailureGuard решает, когда прогон пора останавливать.
	FailureGuard = pagebatch.FailureGuard
)

// ErrRunStopped — прогон остановлен предохранителем, а не дошёл до конца пачки.
//
// Переменная, а не копия: значение то же самое, поэтому errors.Is по нему продолжает работать
// у всех, кто ловил его раньше.
var ErrRunStopped = pagebatch.ErrRunStopped

// Пороги предохранителя. См. pagebatch.
const (
	DefaultSameReasonLimit = pagebatch.DefaultSameReasonLimit
	DefaultFailureLimit    = pagebatch.DefaultFailureLimit
)

// Разбор входа и предохранитель. Сигнатуры те же, что были до переезда.
var (
	ParseSources     = pagebatch.ParseSources
	ParseWorkbook    = pagebatch.ParseWorkbook
	ReadSources      = pagebatch.ReadSources
	ResolveInputFile = pagebatch.ResolveInputFile
	NewFailureGuard  = pagebatch.NewFailureGuard
)
