package google

// Селекторы и адреса Google собраны в одном месте: вёрстка чужого сайта меняется без предупреждения.
const (
	// createDocumentURLTemplate открывает новый пустой документ, уже лежащий в папке.
	createDocumentURLTemplate = "https://docs.google.com/document/create?folder=%s"
	// searchInFolderURLTemplate ищет по имени внутри папки запросом в адресе, без автодополнения поля.
	searchInFolderURLTemplate = "https://drive.google.com/drive/search?q=%s%%20parent:%s"

	// signInURLMarker — признак страницы входа в адресе.
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
	// documentCanvas — область текста документа; клик по ней ставит курсор.
	documentCanvas = `.kix-appview-editor, .kix-canvas-tile-content`
	// documentSavedMarker — индикатор «все изменения сохранены на Диске».
	documentSavedMarker = `[aria-label*="Сохранено на Диске"], [aria-label*="Saved to Drive"], .docs-save-indicator-saved`
	// driveSearchResultRow — строка результата поиска в Drive; в табличной вёрстке это tr[role="row"].
	driveSearchResultRow = `tr[role="row"][data-id], div[role="row"][data-id], div[role="listitem"][data-id]`
	// driveSearchResultName — имя файла внутри строки результата.
	driveSearchResultName = `[data-tooltip], [aria-label]`
)

// visibleElementJS — условие видимости элемента для ожидания на DOM вместо sleep.
const visibleElementJS = `selector => {
	const node = document.querySelector(selector);
	if (!node) { return false; }
	const style = window.getComputedStyle(node);
	if (style.visibility === 'hidden' || style.display === 'none') { return false; }
	const box = node.getBoundingClientRect();
	return box.width > 0 && box.height > 0;
}`

// writeClipboardJS кладёт текст в буфер обмена.
const writeClipboardJS = `text => navigator.clipboard.writeText(text)`

// writeRichClipboardJS кладёт в буфер текст и HTML одним элементом; Docs берёт оформление из HTML.
const writeRichClipboardJS = `([text, html]) => navigator.clipboard.write([new ClipboardItem({
	'text/plain': new Blob([text], {type: 'text/plain'}),
	'text/html': new Blob([html], {type: 'text/html'}),
})])`
