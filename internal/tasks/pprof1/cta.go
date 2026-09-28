package pprof1

import (
	"fmt"
	"html"
	"os"
	"regexp"
	"sort"
	"strings"
	"text/template"
)

// CTACardPath — шаблон карточки призыва, последнего элемента статьи.
//
// Карточку ставит код, а не модель: у неё фиксированные инлайновые стили в цветах dpoprof.ru
// и классы sp-cta-card и sp-cta-button. Вёрстку, подписи плашек, шаги «Как начать обучение»
// и надпись кнопки держит шаблон; у модели спрашиваются только строки, которые меняются от
// статьи к статье.
//
// Заголовок раздела и абзац перед карточкой пишет модель: это обычный последний H2 статьи,
// он стоит в структуре и проходит редактуру как часть текста.
const CTACardPath = "tasks/pprof_1/templates/cta_card.html"

// Признаки уже нарисованного призыва. Промпт его запрещает, но если модель нарисовала свой,
// второго ставить нельзя.
const (
	ctaButtonMarker = "sp-cta-button"
	ctaCardMarker   = "sp-cta-card"
)

// ctaCard — слоты шаблона карточки. Имена полей совпадают с плейсхолдерами файла: разойдутся —
// шаблон подставит пустую строку, а не откажет, поэтому набор проверяется тестом.
type ctaCard struct {
	Badge         string
	Heading       string
	Intro         string
	Qualification string
	Document      string
	Format        string
	ButtonURL     string
}

// readCTACard читает и разбирает шаблон карточки.
//
// Разбирается он до первого сообщения модели: отказ файла обязан стоить нисколько, а не
// оплаченный ответ разметки.
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

// ctaSlotsPrompt — сообщение, которым у модели спрашиваются строки карточки.
//
// Статья уже в истории чата разметки, второй раз её передавать нельзя — упрётся в предел
// длины ответа. Адреса среди строк нет: он приходит колонкой course_url книги импорта.
func ctaSlotsPrompt(title, courseURL string) string {
	return fmt.Sprintf(`Статья «%s» заканчивается карточкой учебного центра ДПО ПРОФ с кнопкой «Записаться на обучение». Вёрстку карточки, шаги записи и надпись кнопки ставит код — от тебя нужны только строки.

Кнопка ведёт сюда: %s — строки пиши про эту программу (если адрес ведёт на раздел сайта, а не на одну программу, — про обучение по теме статьи в целом).

Ответь ровно шестью строками в формате «ключ ;; значение», без нумерации, без пояснений и без кодового блока:

бейдж ;; вид обучения, 2–4 слова, например «Повышение квалификации», «Профессиональное обучение», «Обучение по охране труда»
заголовок ;; призыв с пользой для читателя, 5–9 слов, глагол в повелительном наклонении, например «Повысьте разряд электрогазосварщика без отрыва от работы»
описание ;; чему учит программа и что читатель получит на выходе, 25–40 слов, одним абзацем, по фактам статьи; без обещаний трудоустройства, скидок и сроков, которых нет в статье
квалификация ;; что получает выпускник, 3–6 слов, например «Разряды с 2-го по 6-й», «Профессия рабочего с разрядом»
документ ;; какой документ выдаётся и куда вносятся сведения о нём, 3–6 слов, например «Удостоверение, сведения в ФИС ФРДО»; для охраны труда и других проверок знаний — свой реестр, не ФИС ФРДО
формат ;; как проходит обучение, 3–6 слов, например «Дистанционно, в своём темпе»`, title, courseURL)
}

// ctaNumberingRE — нумерация и маркер списка в начале строки: «1. », «2) », «- », «• ».
var ctaNumberingRE = regexp.MustCompile(`^(?:[-*•]\s*)?(?:\d+[.)]\s*)?`)

// ctaSlotKeys — ключи, которые разбирает parseCTASlots. Список закрытый: строку с чужим
// ключом разбор молча пропускает.
var ctaSlotKeys = map[string]struct{}{
	"бейдж": {}, "заголовок": {}, "описание": {}, "квалификация": {}, "документ": {}, "формат": {},
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
		key = strings.ToLower(strings.TrimSpace(ctaNumberingRE.ReplaceAllString(strings.TrimSpace(key), "")))
		value = strings.Trim(strings.TrimSpace(value), "«»\"")
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
// Недостающий слот стадию не роняет: за генерацию уже заплачено, и статья с типовой
// карточкой полезнее отказа. Каждое умолчание уходит в лог — по нему видно, что промпт не
// сработал.
func buildCTACard(slots map[string]string, courseURL string) (ctaCard, []string) {
	card := defaultCTACard()
	var missing []string

	for key, field := range map[string]*string{
		"бейдж":        &card.Badge,
		"заголовок":    &card.Heading,
		"описание":     &card.Intro,
		"квалификация": &card.Qualification,
		"документ":     &card.Document,
		"формат":       &card.Format,
	} {
		if value, ok := slots[key]; ok {
			*field = html.EscapeString(value)
		} else {
			missing = append(missing, key)
		}
	}
	card.ButtonURL = html.EscapeString(courseURL)
	sort.Strings(missing)
	return card, missing
}

// defaultCTACard — карточка, которую ставит код, когда модель не ответила.
//
// Строки нарочно общие: они верны для любой статьи блога и ничего не обещают сверх того, что
// учебный центр правда делает. Конкретику даёт модель, а умолчание — страховка.
func defaultCTACard() ctaCard {
	return ctaCard{
		Badge:         "Обучение в ДПО ПРОФ",
		Heading:       "Получите профессию без отрыва от работы",
		Intro:         "ДПО ПРОФ ведёт профессиональное обучение, переподготовку и повышение квалификации по лицензии. Теорию вы проходите дистанционно, а после итоговой аттестации получаете документ установленного образца.",
		Qualification: "Профессия или разряд по ЕТКС",
		Document:      "Документ в госреестре",
		Format:        "Дистанционно, в своём темпе",
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

// appendCTACard дописывает карточку в конец разметки. Если призыв в разметке уже есть, свою
// не дописывает: двух подряд быть не должно.
func appendCTACard(markup, card string) (string, bool) {
	if strings.Contains(markup, ctaCardMarker) || strings.Contains(markup, ctaButtonMarker) {
		return markup, false
	}
	return strings.TrimSpace(markup) + "\n\n" + card, true
}
