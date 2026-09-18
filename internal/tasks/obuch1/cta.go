package obuch1

import (
	"fmt"
	"html"
	"os"
	"regexp"
	"strings"
	"text/template"
)

// CTAButtonPath — шаблон кнопки призыва, последнего элемента статьи.
//
// Путь относительно корня проекта, как у кнопки заявки pprof_2. Кнопку ставит код, а не
// модель: у неё фиксированные инлайновые стили и класс sp-cta-button, за который цепляется
// оформление площадки, — модель воспроизводила бы их каждый раз по-своему. На этой площадке
// кнопка вообще единственное место в теле статьи, где инлайновый стиль законен.
//
// Заголовок раздела и абзац перед кнопкой пишет модель: это обычный последний H2 статьи,
// он стоит в структуре и читается как её часть, а не как приклеенный снизу баннер.
const CTAButtonPath = "tasks/obuch_1/templates/cta_button.html"

// ctaFallbackURL — адрес кнопки, когда модель не назвала свой.
//
// Раздел рабочих профессий подходит любой статье площадки: он выше конкретной программы и
// потому не врёт. Придумывать адрес программы код не вправе — несуществующая ссылка в
// опубликованной статье хуже, чем ссылка на раздел.
const ctaFallbackURL = "https://obuchim-specialista.ru/rabochie-professii/"

// ctaButtonMarker — признак уже нарисованной кнопки. Промпт её запрещает, но если модель
// нарисовала свою, вторую ставить нельзя.
const ctaButtonMarker = "sp-cta-button"

// ctaButton — слоты шаблона кнопки. Имена полей совпадают с плейсхолдерами файла: разойдутся —
// шаблон подставит пустую строку, а не откажет, поэтому набор проверяется тестом.
type ctaButton struct {
	ButtonURL  string
	ButtonText string
}

// readCTAButton читает и разбирает шаблон кнопки.
//
// Разбирается он до первого сообщения модели: отказ файла обязан стоить нисколько, а не
// оплаченный ответ разметки. Пустой файл — отказ: статья без кнопки обрывается на абзаце с
// призывом, за которым читателю некуда нажать.
func readCTAButton(path string) (*template.Template, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("шаблон кнопки призыва не прочитан (%s): %w", path, err)
	}
	if strings.TrimSpace(string(raw)) == "" {
		return nil, fmt.Errorf("файл кнопки призыва пуст: %s", path)
	}
	parsed, err := template.New("cta").Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("шаблон кнопки призыва не разобран (%s): %w", path, err)
	}
	return parsed, nil
}

// ctaSlotsPrompt — сообщение, которым у модели спрашиваются надпись и адрес кнопки.
//
// Промпт зашит в коде, как RepairLinksPrompt: статья уже в истории этого чата, второй раз её
// передавать нельзя — упрётся в предел длины ответа. Просятся ровно две строки: вёрстку
// ставит код, а текст раздела модель уже написала выше.
func ctaSlotsPrompt(title string) string {
	return fmt.Sprintf(`Последний раздел статьи «%s» заканчивается кнопкой на программу обучения. Её вёрстку ставит код — от тебя нужны только надпись и адрес.

Ответь ровно двумя строками в формате «ключ ;; значение», без нумерации, без пояснений и без кодового блока:

кнопка ;; надпись на кнопке, 2–4 слова, глагол в повелительном наклонении
адрес ;; адрес страницы программы или раздела на сайте, из тех, что были в списке перелинковки

Адрес бери из списка перелинковки дословно. Своего не придумывай: несуществующая ссылка в опубликованной статье хуже, чем ссылка на раздел.`, title)
}

// ctaNumberingRE — нумерация и маркер списка в начале строки: «1. », «2) », «- », «• ».
// Модель добавляет их к ответу сама, и ключ из-за них перестаёт узнаваться.
var ctaNumberingRE = regexp.MustCompile(`^(?:[-*•]\s*)?(?:\d+[.)]\s*)?`)

// ctaSlotKeys — ключи, которые разбирает parseCTASlots. Список закрытый: строку с чужим
// ключом разбор молча пропускает, как это делает ParseLinkInserts с непригодной строкой.
var ctaSlotKeys = map[string]struct{}{"кнопка": {}, "адрес": {}}

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

// buildCTAButton собирает кнопку из разобранных слотов и возвращает имена тех, что пришлось
// заменить умолчанием.
//
// Недостающий слот стадию не роняет: за генерацию уже заплачено, и статья с типовой кнопкой
// полезнее отказа. Каждое умолчание уходит в лог — по нему видно, что промпт не сработал.
func buildCTAButton(slots map[string]string, fallbackURL string) (ctaButton, []string) {
	defaults := defaultCTAButton(fallbackURL)
	button := defaults
	var missing []string

	if text, ok := slots["кнопка"]; ok {
		button.ButtonText = html.EscapeString(text)
	} else {
		missing = append(missing, "кнопка")
	}
	button.ButtonURL = ctaURL(slots["адрес"], fallbackURL)
	if _, ok := slots["адрес"]; !ok {
		missing = append(missing, "адрес")
	}
	return button, missing
}

// ctaURL проверяет адрес кнопки: в кнопку уходит только http-адрес.
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

// defaultCTAButton — кнопка, которую ставит код, когда модель не ответила вовсе.
//
// Надпись нарочно общая: она верна для любой статьи площадки и ничего не обещает сверх того,
// что площадка правда делает. Конкретику даёт модель, а умолчание — страховка, а не замена.
func defaultCTAButton(fallbackURL string) ctaButton {
	return ctaButton{ButtonURL: fallbackURL, ButtonText: "Выбрать программу обучения"}
}

// renderCTAButton подставляет слоты в шаблон.
func renderCTAButton(tmpl *template.Template, button ctaButton) (string, error) {
	var out strings.Builder
	if err := tmpl.Execute(&out, button); err != nil {
		return "", fmt.Errorf("кнопка призыва не собрана: %w", err)
	}
	return strings.TrimSpace(out.String()), nil
}

// appendCTAButton дописывает кнопку в конец разметки.
//
// Если кнопка в разметке уже есть, своя не дописывается: двух подряд быть не должно, а
// нарисованная моделью всё же стоит на своём месте. Так же устроена кнопка заявки у pprof_2.
func appendCTAButton(markup, button string) (string, bool) {
	if strings.Contains(markup, ctaButtonMarker) {
		return markup, false
	}
	return strings.TrimSpace(markup) + "\n\n" + button, true
}
