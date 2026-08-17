package google

// Селекторы и адреса Google собраны здесь, а не размазаны по коду: вёрстка чужого сайта
// меняется без предупреждения, и чинить её нужно в одном месте.
//
// ВНИМАНИЕ. Эти селекторы написаны по публично известной разметке Drive и Docs, но **не
// проверены на живом аккаунте** — у сборки не было сессии Google. Первый прогон
// google-publish может упасть именно здесь. Диагностика каждой неудачи (screenshot.png,
// page.html, info.json) ложится в каталог диагностики задачи (DiagnosticsDir/google), а чинить кандидатов удобнее
// через /selector-fix: он открывает страницу Chrome DevTools MCP и проверяет селектор
// вживую, вместо угадывания по сохранённому дампу.
const (
	// createDocumentURLTemplate создаёт документ сразу в нужной папке. Переход по этому
	// адресу открывает новый пустой документ, уже лежащий в folder.
	createDocumentURLTemplate = "https://docs.google.com/document/create?folder=%s"
	// searchInFolderURLTemplate ищет по точному имени внутри папки. Поиск идёт запросом в
	// адресе, а не набором в поле: так не нужно ждать подсказки и бороться с автодополнением.
	searchInFolderURLTemplate = "https://drive.google.com/drive/search?q=%s%%20parent:%s"

	// signInURLMarker и challengeURLMarkers опознают состояния, из которых автоматика выйти
	// не может. Проверяются по адресу страницы, а не по тексту: текст локализуется.
	signInURLMarker = "accounts.google.com"
)

// challengeURLMarkers — признаки CAPTCHA и 2FA в адресе страницы.
var challengeURLMarkers = []string{
	"/signin/challenge",
	"/challenge/",
	"/sorry/",
	"recaptcha",
}

const (
	// documentTitleInput — поле имени документа в шапке Docs.
	documentTitleInput = `input.docs-title-input, input[aria-label="Rename"]`
	// documentCanvas — область текста документа. Клик по ней ставит курсор.
	documentCanvas = `.kix-appview-editor, .kix-canvas-tile-content`
	// documentSavedMarker — индикатор «все изменения сохранены на Диске».
	documentSavedMarker = `[aria-label*="Сохранено на Диске"], [aria-label*="Saved to Drive"], .docs-save-indicator-saved`
	// driveSearchResultRow — строка результата поиска в Drive.
	driveSearchResultRow = `div[role="row"][data-id], div[role="listitem"][data-id]`
	// driveSearchResultName — имя файла внутри строки результата.
	driveSearchResultName = `[data-tooltip], [aria-label]`
)

// visibleElementJS повторяет приём deepseekweb: ожидание пишется как условие на DOM, а не как
// sleep, поэтому оно завершается ровно тогда, когда состояние наступило.
const visibleElementJS = `selector => {
	const node = document.querySelector(selector);
	if (!node) { return false; }
	const style = window.getComputedStyle(node);
	if (style.visibility === 'hidden' || style.display === 'none') { return false; }
	const box = node.getBoundingClientRect();
	return box.width > 0 && box.height > 0;
}`

// writeClipboardJS кладёт текст в буфер обмена. Промпт вставляется, а не набирается: набор
// текста на десятки тысяч символов занимает минуты и рвётся на автозамене Docs.
const writeClipboardJS = `text => navigator.clipboard.writeText(text)`
