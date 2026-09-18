package obuch1

import (
	"fmt"
	"html"
	"os"
	"regexp"
	"strings"
	"text/template"
)

// CTACardPath — шаблон карточки призыва, последнего блока статьи.
//
// Путь относительно корня проекта, как у кнопки заявки pprof_2. Карточку ставит код, а не
// модель: на 25 опубликованных статьях её скелет побайтово одинаков, единственное расхождение
// во всей разметке — адрес кнопки. Три с половиной тысячи знаков инлайновых стилей в ответе
// съедают предел длины сообщения и каждый раз воспроизводятся по-своему.
const CTACardPath = "tasks/obuch_1/templates/cta_card.html"

// ctaFallbackURL — адрес кнопки, когда модель не назвала свой.
//
// Раздел рабочих профессий подходит любой статье площадки: он выше конкретной программы и
// потому не врёт. Придумывать адрес программы код не вправе — несуществующая ссылка в
// опубликованной статье хуже, чем ссылка на раздел.
const ctaFallbackURL = "https://obuchim-specialista.ru/rabochie-professii/"

// ctaCardMarker — признак уже нарисованной карточки. Промпт её запрещает, но если модель
// нарисовала свою, второй ставить нельзя.
const ctaCardMarker = "sp-cta-card"

// ctaCard — слоты шаблона карточки. Имена полей совпадают с плейсхолдерами файла: разойдутся —
// шаблон подставит пустую строку, а не откажет, поэтому набор проверяется тестом.
type ctaCard struct {
	Badge        string
	Title        string
	Lead         string
	Tile1Caption string
	Tile1Value   string
	Tile2Caption string
	Tile2Value   string
	Tile3Caption string
	Tile3Value   string
	ButtonURL    string
	ButtonText   string
	Note         string
}

// readCTACard читает и разбирает шаблон карточки.
//
// Разбирается он до первого сообщения модели: отказ файла обязан стоить нисколько, а не
// оплаченный ответ разметки. Пустой файл — отказ: пустая карточка означала бы статью без
// призыва, а он есть у всех статей площадки.
func readCTACard(path string) (*template.Template, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("шаблон карточки призыва не прочитан (%s): %w", path, err)
	}
	if strings.TrimSpace(string(raw)) == "" {
		return nil, fmt.Errorf("файл карточки призыва пуст: %s", path)
	}
	parsed, err := template.New("cta").Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("шаблон карточки призыва не разобран (%s): %w", path, err)
	}
	return parsed, nil
}

// ctaSlotsPrompt — сообщение, которым у модели спрашиваются тексты карточки.
//
// Промпт зашит в коде, как RepairLinksPrompt: статья уже в истории этого чата, второй раз её
// передавать нельзя — упрётся в предел длины ответа. Просится ровно девять строк, а не
// карточка: вёрстку ставит код, и от модели нужны только слова.
func ctaSlotsPrompt(title string) string {
	return fmt.Sprintf(`Под статьёй «%s» стоит карточка с приглашением на обучение. Её вёрстку ставит код — от тебя нужны только тексты.

Ответь ровно девятью строками в формате «ключ ;; значение», без нумерации, без пояснений и без кодового блока:

пилюля ;; три коротких пункта через « • »: нормативная база и формат обучения по теме статьи
заголовок ;; чему учим, одной строкой в 5–9 слов
лид ;; одно-два предложения: что даёт обучение читателю этой статьи
плашка1 ;; подпись | значение — про документ, который получает выпускник
плашка2 ;; подпись | значение — про реестр или законность документа
плашка3 ;; подпись | значение — про формат или срок обучения
адрес ;; адрес страницы программы или раздела на сайте, из тех, что были в списке перелинковки
кнопка ;; надпись на кнопке, 2–4 слова, глагол в повелительном наклонении
примечание ;; короткая строка рядом с кнопкой: что будет после заявки

Подпись плашки — 1–2 слова («Документ», «ФИС ФРДО», «Формат»), значение — 3–7 слов. Ничего не выдумывай про сроки и цены, если этого нет в статье.`, title)
}

// ctaNumberingRE — нумерация и маркер списка в начале строки: «1. », «2) », «- », «• ».
// Модель добавляет их к ответу сама, и ключ из-за них перестаёт узнаваться.
var ctaNumberingRE = regexp.MustCompile(`^(?:[-*•]\s*)?(?:\d+[.)]\s*)?`)

// ctaSlotKeys — ключи, которые разбирает parseCTASlots. Список закрытый: строку с чужим
// ключом разбор молча пропускает, как это делает ParseLinkInserts с непригодной строкой.
var ctaSlotKeys = map[string]struct{}{
	"пилюля": {}, "заголовок": {}, "лид": {},
	"плашка1": {}, "плашка2": {}, "плашка3": {},
	"адрес": {}, "кнопка": {}, "примечание": {},
}

// parseCTASlots разбирает ответ модели построчно.
//
// Строка без разделителя, с неизвестным ключом или с пустым значением пропускается молча:
// модель добавляет к ответу нумерацию, вступление и кодовую обёртку, и ронять из-за них
// оплаченную разметку незачем. Недостающее потом заменяется умолчаниями.
func parseCTASlots(answer string) map[string]string {
	slots := make(map[string]string, len(ctaSlotKeys))
	for _, line := range strings.Split(answer, "\n") {
		line = strings.TrimSpace(strings.ReplaceAll(line, "`", ""))
		key, value, found := strings.Cut(line, ";;")
		if !found {
			continue
		}
		// Нумерация снимается только с начала строки: strings.Trim с теми же символами в
		// наборе съел бы цифру и с конца ключа, и «плашка1» стала бы «плашка».
		key = strings.ToLower(strings.TrimSpace(ctaNumberingRE.ReplaceAllString(strings.TrimSpace(key), "")))
		value = strings.TrimSpace(value)
		if _, known := ctaSlotKeys[key]; !known || value == "" {
			continue
		}
		if _, duplicate := slots[key]; duplicate {
			continue
		}
		slots[key] = value
	}
	return slots
}

// buildCTACard собирает карточку из разобранных слотов и возвращает имена тех, что пришлось
// заменить умолчанием.
//
// Недостающий слот стадию не роняет: за генерацию уже заплачено, и статья с типовой карточкой
// полезнее отказа. Каждое умолчание уходит в лог — по нему видно, что промпт не сработал.
func buildCTACard(slots map[string]string, fallbackURL string) (ctaCard, []string) {
	defaults := defaultCTACard(fallbackURL)
	card := defaults
	var missing []string

	take := func(key string, target *string, fallback string) {
		if value, ok := slots[key]; ok {
			*target = html.EscapeString(value)
			return
		}
		*target = fallback
		missing = append(missing, key)
	}
	takeTile := func(key string, caption, value *string, captionFallback, valueFallback string) {
		raw, ok := slots[key]
		if !ok {
			*caption, *value = captionFallback, valueFallback
			missing = append(missing, key)
			return
		}
		left, right, found := strings.Cut(raw, "|")
		if !found || strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" {
			*caption, *value = captionFallback, valueFallback
			missing = append(missing, key)
			return
		}
		*caption = html.EscapeString(strings.TrimSpace(left))
		*value = html.EscapeString(strings.TrimSpace(right))
	}

	take("пилюля", &card.Badge, defaults.Badge)
	take("заголовок", &card.Title, defaults.Title)
	take("лид", &card.Lead, defaults.Lead)
	takeTile("плашка1", &card.Tile1Caption, &card.Tile1Value, defaults.Tile1Caption, defaults.Tile1Value)
	takeTile("плашка2", &card.Tile2Caption, &card.Tile2Value, defaults.Tile2Caption, defaults.Tile2Value)
	takeTile("плашка3", &card.Tile3Caption, &card.Tile3Value, defaults.Tile3Caption, defaults.Tile3Value)
	take("кнопка", &card.ButtonText, defaults.ButtonText)
	take("примечание", &card.Note, defaults.Note)
	card.ButtonURL = ctaURL(slots["адрес"], fallbackURL)
	if _, ok := slots["адрес"]; !ok {
		missing = append(missing, "адрес")
	}
	return card, missing
}

// ctaURL проверяет адрес кнопки: в карточку уходит только http-адрес.
//
// Всё остальное — «см. сайт», выдуманный путь без домена, пустая строка — заменяется
// разделом. Битая ссылка в опубликованной статье дороже, чем ссылка на раздел.
func ctaURL(value, fallbackURL string) string {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://") {
		return fallbackURL
	}
	return html.EscapeString(value)
}

// defaultCTACard — карточка, которую ставит код, когда модель не ответила вовсе.
//
// Тексты нарочно общие: они верны для любой статьи площадки и ничего не обещают сверх того,
// что она правда делает. Конкретику даёт модель, а умолчание — страховка, а не замена.
func defaultCTACard(fallbackURL string) ctaCard {
	return ctaCard{
		Badge:        "Профстандарты • 273-ФЗ • Дистанционное обучение",
		Title:        "Профессиональное обучение востребованным специальностям",
		Lead:         "Освойте профессию по программе учебного центра и получите документ, который признают работодатели.",
		Tile1Caption: "Документ",
		Tile1Value:   "Свидетельство о профессии и разряд",
		Tile2Caption: "ФИС ФРДО",
		Tile2Value:   "Сведения о документе вносятся в реестр",
		Tile3Caption: "Формат",
		Tile3Value:   "Дистанционно, без отрыва от работы",
		ButtonURL:    fallbackURL,
		ButtonText:   "Выбрать программу обучения",
		Note:         "Консультация методиста по выбору направления за 15 минут",
	}
}

// renderCTACard подставляет слоты в шаблон.
func renderCTACard(tmpl *template.Template, card ctaCard) (string, error) {
	var out strings.Builder
	if err := tmpl.Execute(&out, card); err != nil {
		return "", fmt.Errorf("карточка призыва не собрана: %w", err)
	}
	return strings.TrimSpace(out.String()), nil
}

// appendCTACard дописывает карточку в конец разметки.
//
// Если карточка в разметке уже есть, своя не дописывается: двух подряд быть не должно, а
// нарисованная моделью всё же стоит на своём месте. Так же устроена кнопка заявки у pprof_2.
func appendCTACard(markup, card string) (string, bool) {
	if strings.Contains(markup, ctaCardMarker) {
		return markup, false
	}
	return strings.TrimSpace(markup) + "\n\n" + card, true
}
