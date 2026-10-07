// Package catalog хранит каталог услуг площадки: что она продаёт, как это разложено по
// рубрикам и какая услуга подходит статье.
//
// Каталог принадлежит площадке, а не задаче; у каждой площадки своя схема (site, site_obuchim),
// различия при сборе — в site.go. Источник каталога — интерфейс у потребителя, хранилище на
// pgx — postgres.go; разбор названий и подбор услуг — чистые функции.
package catalog

// Industry — рубрика площадки, как её завела сама площадка.
type Industry struct {
	// Taxonomy — таксономия рубрики (obuch-cat, perepod-cat). Слаги в разных таксономиях
	// повторяются, поэтому рубрика опознаётся парой «таксономия + термин».
	Taxonomy string
	TermID   int64
	Slug     string
	Name     string
}

// Profession — наша рубрика: кем человек станет, пройдя услугу. Дробнее рубрик площадки,
// по которым три ссылки под статью не подобрать.
type Profession struct {
	// Slug — латинский ключ профессии (svarshhik).
	Slug string
	// Name — человеческое имя («Сварщик»).
	Name string
	// Aliases — основы слов, по которым профессия узнаётся во входных данных статьи.
	Aliases []string
}

// Program — одна услуга площадки.
type Program struct {
	// PostID — идентификатор записи WordPress; он уходит в связь related_courses.
	PostID   int64
	PostType string
	// Category и Priority — тип услуги словами и порядок предложения читателю.
	Category string
	Priority int
	Slug     string
	URL      string
	// Title — заголовок записи целиком, Name — короткое название до тире, годное анкором.
	Title string
	Name  string
	// Industry и Profession — рубрика площадки и наша профессия; услуга с пустым слагом
	// профессии в подбор не попадает.
	Industry   Industry
	Profession Profession
}
