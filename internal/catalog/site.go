package catalog

// Площадка каталога: типы услуг, имя таксономии рубрик и источник профессии.
// Данные в коде, а не в конфиге: новый тип услуги — это новый раздел сайта.

type programType struct {
	postType string
	category string
	priority int
}

// Site — описание площадки для сбора каталога.
type Site struct {
	key   string
	types []programType
	// taxonomy — имя таксономии рубрик у типа записи: obuch-cat против cat_rabprof.
	taxonomy func(postType string) string
	// profession — откуда берётся наша рубрика: из названия услуги или из рубрики площадки.
	profession func(post SourcePost, industry Industry) Profession
}

// Key возвращает ключ площадки, которым задача называет свой каталог.
func (s Site) Key() string { return s.key }

// PostTypes возвращает типы записей площадки в порядке сбора.
func (s Site) PostTypes() []string {
	types := make([]string, 0, len(s.types))
	for _, item := range s.types {
		types = append(types, item.postType)
	}
	return types
}

// Taxonomy отдаёт имя таксономии рубрик для типа записи; false — типа у площадки нет,
// хотя правило имени дало бы правдоподобную строку для любого.
func (s Site) Taxonomy(postType string) (string, bool) {
	for _, item := range s.types {
		if item.postType == postType {
			return s.taxonomy(postType), true
		}
	}
	return "", false
}

func (s Site) describe(postType string) (string, int, bool) {
	for _, item := range s.types {
		if item.postType == postType {
			return item.category, item.priority, true
		}
	}
	return "", 0, false
}

// dpoprofTypes — типы услуг dpoprof.ru в порядке предложения читателю: сначала вход в
// профессию, не алфавит.
var dpoprofTypes = []programType{
	{postType: "obuch", category: "Обучение", priority: 1},
	{postType: "perepod", category: "Переподготовка", priority: 2},
	{postType: "povysh", category: "Повышение квалификации", priority: 3},
	{postType: "obuch_med", category: "Медицинское обучение", priority: 4},
	{postType: "bezopasnost", category: "Безопасность", priority: 5},
	{postType: "attestaciya", category: "Аттестация", priority: 6},
}

// obuchimTypes — типы услуг obuchim-specialista.ru в том же порядке «сначала вход в профессию».
var obuchimTypes = []programType{
	{postType: "rabprof", category: "Рабочие профессии", priority: 1},
	{postType: "perepodgotovka", category: "Переподготовка", priority: 2},
	{postType: "povyshenie", category: "Повышение квалификации", priority: 3},
	{postType: "akkreditaciya", category: "Аккредитация", priority: 4},
	{postType: "medpersonal", category: "Обучение медперсонала", priority: 5},
	{postType: "attestaciya", category: "Аттестация", priority: 6},
}

// DPOProf возвращает площадку dpoprof.ru; её рубрики крупные, профессия выводится из названия услуги.
func DPOProf() Site {
	return Site{
		key:      SiteDPOProf,
		types:    dpoprofTypes,
		taxonomy: func(postType string) string { return postType + "-cat" },
		profession: func(post SourcePost, _ Industry) Profession {
			return ProfessionOf(post.Title)
		},
	}
}

// Obuchim возвращает площадку obuchim-specialista.ru; её рубрики дробные и служат профессией,
// а разбор названий здесь не годится: профессия в них стоит в конце, в винительном падеже.
func Obuchim() Site {
	return Site{
		key:      SiteObuchim,
		types:    obuchimTypes,
		taxonomy: func(postType string) string { return "cat_" + postType },
		profession: func(_ SourcePost, industry Industry) Profession {
			return ProfessionFromIndustry(industry)
		},
	}
}

// Ключи площадок, которыми их называет профиль задачи.
const (
	SiteDPOProf = "dpoprof"
	SiteObuchim = "obuchim"
)

// PostTypes возвращает типы записей dpoprof.ru; их же берут задачи аудита.
func PostTypes() []string { return DPOProf().PostTypes() }
