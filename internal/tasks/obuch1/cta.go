package obuch1

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
// Карточку ставит код, а не модель: у неё фиксированные инлайновые стили и классы
// sp-cta-card и sp-cta-button, за которые цепляется оформление площадки. Снято с живых
// статей блога (kuznecz, stekloduv, professiya-ispytatel, professiya-shlifovshhik): у всех
// четырёх карточка одна и та же, меняются только бейдж, заголовок, абзац, плашка
// квалификации и надпись кнопки — их и спрашиваем у модели, остальное держит шаблон.
//
// Заголовок раздела и абзац перед карточкой пишет модель: это обычный последний H2 статьи,
// он стоит в структуре и читается как её часть, а не как приклеенный снизу баннер.
const CTACardPath = "tasks/obuch_1/templates/cta_card.html"

// ctaButtonMarker и ctaCardMarker — признаки уже нарисованного призыва. Промпт его
// запрещает, но если модель нарисовала свой, второго ставить нельзя.
const (
	ctaButtonMarker = "sp-cta-button"
	// Класс карточки — тот же, что на живых статьях блога: светлая плашка с бейджем,
	// тремя плашками и оранжевой кнопкой-ссылкой.
	ctaCardMarker = "sp-cta-card"
)

// ctaCard — слоты шаблона карточки. Имена полей совпадают с плейсхолдерами файла:
// разойдутся — шаблон подставит пустую строку, а не откажет, поэтому набор проверяется
// тестом. Плашки «ФИС ФРДО» и «Формат подготовки» слотов не имеют: на живых страницах они
// у всех статей одинаковые, и спрашивать у модели то, что не меняется, незачем.
type ctaCard struct {
	Badge         string
	Heading       string
	Intro         string
	Qualification string
	ButtonURL     string
	ButtonText    string
}

// readCTACard читает и разбирает шаблон карточки.
//
// Разбирается он до первого сообщения модели: отказ файла обязан стоить нисколько, а не
// оплаченный ответ разметки. Пустой файл — отказ: статья без карточки обрывается на абзаце
// с призывом, за которым читателю некуда нажать.
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

// ctaSlotsPrompt — сообщение, которым у модели спрашиваются строки карточки призыва.
//
// Промпт зашит в коде, как RepairLinksPrompt: статья уже в истории этого чата, второй раз
// её передавать нельзя — упрётся в предел длины ответа. Просятся только те строки, которые
// на живых страницах блога меняются от статьи к статье; вёрстку, плашку ФИС ФРДО, формат
// подготовки и подпись под кнопкой держит шаблон.
//
// Адреса среди строк нет: он приходит колонкой course_url книги импорта. Кнопка ведёт в
// деньги, и решать, на какой курс уходит читатель, обязан человек — а модель, которую о
// нём спрашивали, выбирала ссылку из перелинковки и вправе была ошибиться.
func ctaSlotsPrompt(title, courseURL string) string {
	return fmt.Sprintf(`Статья «%s» заканчивается карточкой учебного центра с кнопкой на программу обучения. Вёрстку карточки и адрес кнопки ставит код — от тебя нужны только строки.

Кнопка ведёт сюда: %s — надпись пиши под эту программу.

Ответь ровно пятью строками в формате «ключ ;; значение», без нумерации, без пояснений и без кодового блока:

бейдж ;; отрасль и норматив, 3–7 слов, например «Профстандарты металлообработки • ЕТКС Выпуск 2»
заголовок ;; «Профессиональное обучение и аттестация <кого>», родительный падеж множественного числа
описание ;; чему учат и что выдают, 25–45 слов, одним абзацем, без обещаний трудоустройства
квалификация ;; что получает выпускник, 3–6 слов, например «Присвоение 2–6 разрядов по ЕТКС»
кнопка ;; надпись на кнопке, 2–4 слова, глагол в повелительном наклонении`, title, courseURL)
}

// ctaNumberingRE — нумерация и маркер списка в начале строки: «1. », «2) », «- », «• ».
// Модель добавляет их к ответу сама, и ключ из-за них перестаёт узнаваться.
var ctaNumberingRE = regexp.MustCompile(`^(?:[-*•]\s*)?(?:\d+[.)]\s*)?`)

// ctaSlotKeys — ключи, которые разбирает parseCTASlots. Список закрытый: строку с чужим
// ключом разбор молча пропускает, как это делает ParseLinkInserts с непригодной строкой.
var ctaSlotKeys = map[string]struct{}{
	"бейдж": {}, "заголовок": {}, "описание": {}, "квалификация": {}, "кнопка": {},
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
		"кнопка":       &card.ButtonText,
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

// defaultCTACard — карточка, которую ставит код, когда модель не ответила вовсе.
//
// Строки нарочно общие: они верны для любой статьи площадки и ничего не обещают сверх того,
// что площадка правда делает. Конкретику даёт модель, а умолчание — страховка, а не замена.
func defaultCTACard() ctaCard {
	return ctaCard{
		Badge:         "Профстандарты и ЕТКС • 273-ФЗ «Об образовании в РФ»",
		Heading:       "Профессиональное обучение и аттестация рабочих",
		Intro:         "Учебный центр «Обучим Специалиста» ведёт профессиональную подготовку и повышение квалификации по лицензии, выдаёт свидетельства установленного образца и вносит сведения в Федеральный реестр ФИС ФРДО.",
		Qualification: "Свидетельство о профессии рабочего",
		ButtonText:    "Выбрать программу обучения",
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
// Если призыв в разметке уже есть — своей карточкой или одной кнопкой, — свою не
// дописываем: двух подряд быть не должно, а нарисованная моделью всё же стоит на своём
// месте. Так же устроена кнопка заявки у pprof_2.
func appendCTACard(markup, card string) (string, bool) {
	if strings.Contains(markup, ctaCardMarker) || strings.Contains(markup, ctaButtonMarker) {
		return markup, false
	}
	return strings.TrimSpace(markup) + "\n\n" + card, true
}
