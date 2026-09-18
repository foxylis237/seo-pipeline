package catalog

// Площадка каталога: чем один сайт отличается от другого при сборе.
//
// Пакет писался под одну площадку, и три её свойства лежали константами: закрытый список
// типов услуг, правило имени таксономии рубрик и способ вывести профессию из услуги. Со
// второй площадкой они перестали быть свойствами каталога и стали свойствами сайта —
// у неё и типы свои, и таксономия названа наоборот, и профессию выводить из названия не
// нужно вовсе.
//
// Данные площадок лежат здесь, а не в конфиге: тип услуги — это раздел сайта, а не строка
// настроек, и седьмой тип означал бы новый раздел, а не новую запись в YAML.

// programType — тип записи площадки: техническое имя, человеческое название и порядок.
type programType struct {
	postType string
	category string
	priority int
}

// Site — описание площадки для сбора каталога.
//
// Структура, а не интерфейс: различаются площадки данными и двумя правилами, а не
// поведением, и реализовать Site снаружи пакета некому — площадок ровно столько, сколько
// сайтов у проекта.
type Site struct {
	key   string
	types []programType
	// taxonomy — имя таксономии рубрик у типа записи. Правило у площадок разное и
	// зеркальное: obuch-cat против cat_rabprof.
	taxonomy func(postType string) string
	// profession — откуда берётся наша рубрика. Площадке с крупными рубриками её
	// приходится выводить из названия услуги, площадке с дробными — достаточно рубрики.
	profession func(post SourcePost, industry Industry) Profession
}

// Key — ключ площадки. Им задача называет свой каталог, им же он выбирается в composition
// root.
func (s Site) Key() string { return s.key }

// PostTypes возвращает типы записей площадки в порядке сбора.
func (s Site) PostTypes() []string {
	types := make([]string, 0, len(s.types))
	for _, item := range s.types {
		types = append(types, item.postType)
	}
	return types
}

// describe переводит тип записи в человеческое название и порядок.
// Второе значение — false у типа, которого в каталоге этой площадки быть не должно.
func (s Site) describe(postType string) (string, int, bool) {
	for _, item := range s.types {
		if item.postType == postType {
			return item.category, item.priority, true
		}
	}
	return "", 0, false
}

// dpoprofTypes перечисляет типы услуг dpoprof.ru в том порядке, в каком их предлагают
// читателю.
//
// Обучение первое, и это не алфавит: статью про профессию читает чаще всего тот, кто входит
// в неё с нуля. Переподготовка и повышение квалификации адресованы тем, кто уже в
// профессии, и их место следом.
//
// Список закрытый: типов на площадке ровно шесть, и седьмой означал бы новый раздел сайта,
// а не новую строку в конфиге.
var dpoprofTypes = []programType{
	{postType: "obuch", category: "Обучение", priority: 1},
	{postType: "perepod", category: "Переподготовка", priority: 2},
	{postType: "povysh", category: "Повышение квалификации", priority: 3},
	{postType: "obuch_med", category: "Медицинское обучение", priority: 4},
	{postType: "bezopasnost", category: "Безопасность", priority: 5},
	{postType: "attestaciya", category: "Аттестация", priority: 6},
}

// obuchimTypes — типы услуг obuchim-specialista.ru, в том же порядке «сначала вход в
// профессию».
//
// Числа снимались с живой площадки 18.09.2026: rabprof 631, perepodgotovka 459,
// attestaciya 143, povyshenie 121, akkreditaciya 90, medpersonal 84 — всего 1528.
// Аттестация стоит последней по той же причине, что у соседа: она нужна тому, кто уже
// работает, а статьи площадки пишутся для входящих в профессию.
var obuchimTypes = []programType{
	{postType: "rabprof", category: "Рабочие профессии", priority: 1},
	{postType: "perepodgotovka", category: "Переподготовка", priority: 2},
	{postType: "povyshenie", category: "Повышение квалификации", priority: 3},
	{postType: "akkreditaciya", category: "Аккредитация", priority: 4},
	{postType: "medpersonal", category: "Обучение медперсонала", priority: 5},
	{postType: "attestaciya", category: "Аттестация", priority: 6},
}

// DPOProf — площадка dpoprof.ru: та, под которую каталог писался.
//
// Рубрики у неё крупные — 47 рубрик обучения, 218 программ в самой большой, — и подобрать
// по ним три ссылки под статью нельзя. Поэтому профессия выводится из названия услуги
// разбором: 599 рубрик на 1658 услуг.
func DPOProf() Site {
	return Site{
		key:   SiteDPOProf,
		types: dpoprofTypes,
		// Правило площадки: у типа obuch рубрики лежат в obuch-cat, у perepod — в
		// perepod-cat, и так у всех шести. Проверено по картам сайта: обратных примеров нет.
		taxonomy: func(postType string) string { return postType + "-cat" },
		profession: func(post SourcePost, _ Industry) Profession {
			return ProfessionOf(post.Title)
		},
	}
}

// Obuchim — площадка obuchim-specialista.ru.
//
// Здесь разбор названий не нужен, и это измерено: ProfessionOf на её 1528 названиях даёт
// 953 «профессии», 820 из которых по одной услуге, а крупнейшие группы — артефакты разбора
// («профессиональная инженер» из «Дистанционная профессиональная переподготовка на
// инженера»). Названия у площадки другой формы: профессия стоит в конце и в винительном
// падеже, а не до тире.
//
// Зато её собственные рубрики уже дробные: 184 термина на 1528 услуг, медиана — четыре
// программы на рубрику, крупных всего две. То есть площадка сама разметила каталог ровно с
// той гранулярностью, которую у соседа пришлось добывать разбором.
func Obuchim() Site {
	return Site{
		key:   SiteObuchim,
		types: obuchimTypes,
		// Таксономия названа зеркально соседу: cat_rabprof, а не rabprof-cat.
		taxonomy: func(postType string) string { return "cat_" + postType },
		profession: func(_ SourcePost, industry Industry) Profession {
			return ProfessionFromIndustry(industry)
		},
	}
}

// Ключи площадок. Строками они приходят из профиля задачи, поэтому имена объявлены здесь,
// а не переписываются в composition root.
const (
	SiteDPOProf = "dpoprof"
	SiteObuchim = "obuchim"
)

// PostTypes возвращает типы записей dpoprof.ru.
//
// Оставлена отдельной функцией ради задач аудита: их пачки — страницы услуг той же
// площадки, и список типов у них тот же, что у каталога (cmd/seo-pipeline/article_audit.go).
// Второй копии этого списка в composition root быть не должно.
func PostTypes() []string { return DPOProf().PostTypes() }
