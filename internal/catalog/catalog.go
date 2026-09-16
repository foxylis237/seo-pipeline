// Package catalog хранит каталог услуг площадки: что она продаёт, как это разложено по
// рубрикам и какая услуга подходит статье.
//
// Каталог общий для всех задач и ни одной из них не принадлежит: площадка одна, а услуги
// на ней не зависят от того, кто про них пишет. Поэтому и схема PostgreSQL у него своя
// (site), и команда сбора глобальная, как вход в сервисы.
//
// Пакет ничего не знает ни про WordPress, ни про pgx: источник каталога и его хранилище —
// интерфейсы, объявленные здесь же у потребителя. Разбор названий и подбор услуг — чистые
// функции, и проверяются они без сети и без базы.
package catalog

// Industry — рубрика площадки, как её завела сама площадка.
type Industry struct {
	// Taxonomy — таксономия рубрики: obuch-cat, perepod-cat и так далее. Слаги в них
	// повторяются, поэтому рубрика опознаётся парой «таксономия + термин», а не слагом.
	Taxonomy string
	TermID   int64
	Slug     string
	Name     string
}

// Profession — наша собственная рубрика: то, кем человек станет, пройдя услугу.
//
// Она дробнее рубрик площадки. «Аппаратчики и технологические процессы» — 218 программ, и
// подобрать по такой рубрике три ссылки под статью нельзя; профессий же около четырёх с
// половиной сотен, и в каждой единицы услуг.
type Profession struct {
	// Slug — латинский ключ профессии, выведенный из основы слова: svarshhik, montazhnik.
	Slug string
	// Name — человеческое имя, взятое из названия услуги: «Сварщик».
	Name string
	// Aliases — основы слов, по которым профессия узнаётся во входных данных статьи.
	Aliases []string
}

// Program — одна услуга площадки.
type Program struct {
	// PostID — идентификатор записи WordPress. Именно он уходит в связь related_courses,
	// и другого идентификатора у услуги нет.
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
	// Industry и Profession — рубрика площадки и наша профессия. Пустой слаг профессии
	// означает, что разобрать название не удалось; такие услуги в подбор не попадают.
	Industry   Industry
	Profession Profession
}

// programType — тип записи площадки: техническое имя, человеческое название и порядок.
type programType struct {
	postType string
	category string
	priority int
}

// programTypes перечисляет типы услуг в том порядке, в каком их предлагают читателю.
//
// Обучение первое, и это не алфавит: статью про профессию читает чаще всего тот, кто входит
// в неё с нуля. Переподготовка и повышение квалификации адресованы тем, кто уже в
// профессии, и их место следом.
//
// Список закрытый: типов на площадке ровно шесть, и седьмой означал бы новый раздел сайта,
// а не новую строку в конфиге.
var programTypes = []programType{
	{postType: "obuch", category: "Обучение", priority: 1},
	{postType: "perepod", category: "Переподготовка", priority: 2},
	{postType: "povysh", category: "Повышение квалификации", priority: 3},
	{postType: "obuch_med", category: "Медицинское обучение", priority: 4},
	{postType: "bezopasnost", category: "Безопасность", priority: 5},
	{postType: "attestaciya", category: "Аттестация", priority: 6},
}

// PostTypes возвращает типы записей, из которых собирается каталог, в порядке сбора.
func PostTypes() []string {
	types := make([]string, 0, len(programTypes))
	for _, item := range programTypes {
		types = append(types, item.postType)
	}
	return types
}

// describeType переводит тип записи в человеческое название и порядок.
// Второе значение — false у типа, которого в каталоге быть не должно.
func describeType(postType string) (string, int, bool) {
	for _, item := range programTypes {
		if item.postType == postType {
			return item.category, item.priority, true
		}
	}
	return "", 0, false
}
