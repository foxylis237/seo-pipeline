package deepseekweb

import "strings"

const (
	composerSelector = `textarea#chat-input, textarea[placeholder*="DeepSeek" i], textarea[placeholder*="Message" i], [contenteditable="true"][role="textbox"]`
	answerSelector   = `[data-message-author-role="assistant"], [data-role="assistant"], .ds-markdown`
	stopSelector     = `button[aria-label*="stop" i], button[title*="stop" i], [role="button"][aria-label*="stop" i], [data-testid*="stop" i]`
	loginSelector    = `a[href*="sign_in"], a[href*="login"], form input[type="password"]`

	// fileInputSelector — скрытое поле загрузки: «Прикрепить» открывает системный диалог,
	// поэтому файл отдаётся полю напрямую.
	fileInputSelector = `input[type="file"]`

	// Тред DeepSeek — виртуальный список: число ответов на странице не растёт, и новый ответ
	// узнаётся по ключу элемента, который DeepSeek наращивает монотонно.
	itemKeyAttribute = "data-virtual-list-item-key"
	itemSelector     = `[data-virtual-list-item-key]`

	// blockedSelector — разметка страниц проверки и капчи.
	blockedSelector = `#challenge-running, #cf-challenge-running, #cf-wrapper, .cf-browser-verification, .cf-error-title, ` +
		`iframe[src*="challenges.cloudflare.com"], .cf-turnstile, iframe[src*="recaptcha"], iframe[src*="hcaptcha"], ` +
		`.g-recaptcha, .h-captcha, #captcha`
)

const visibleElementJS = `(selector) => Array.from(document.querySelectorAll(selector)).some((element) => {
  const style = window.getComputedStyle(element);
  const rect = element.getBoundingClientRect();
  return style.visibility !== "hidden" && style.display !== "none" && rect.width > 0 && rect.height > 0;
})`

const chatReadyJS = `(options) => {
  const visible = (element) => {
    const style = window.getComputedStyle(element);
    const rect = element.getBoundingClientRect();
    return style.visibility !== "hidden" && style.display !== "none" && rect.width > 0 && rect.height > 0;
  };
  if (Array.from(document.querySelectorAll(options.composerSelector)).some(visible)) return "ready";
  const path = window.location.pathname.toLowerCase();
  if (path.includes("/sign_in") || path.includes("/login")) return "expired";
  if (Array.from(document.querySelectorAll(options.loginSelector)).some(visible)) return "expired";
  return false;
}`

// noticeTextJS — общая часть: текст страницы без содержимого диалога, ответов модели и
// отправленного нами (фраза-маркер блокировки может встретиться в статье или промпте).
// Отправленное вырезается построчно, короткие строки — только целой строкой страницы;
// пробелы схлопываются. Панель навигации DeepSeek рисует сообщение одной строкой вне треда.
const noticeTextJS = `
  const noticeText = () => {
    let page = document.body ? document.body.innerText || "" : "";
    for (const answer of Array.from(document.querySelectorAll(options.answerSelector))) {
      const answerText = answer.innerText || "";
      if (answerText.length > 0) page = page.split(answerText).join(" ");
    }
    // История беседы — наш текст и ответы модели, включая блок рассуждений, которого нет
    // в answerSelector. Плашка отказа приходит в последнем сообщении, поэтому оно остаётся.
    const threadItems = Array.from(document.querySelectorAll(options.itemSelector));
    for (let index = 0; index < threadItems.length - 1; index++) {
      const itemText = threadItems[index].innerText || "";
      if (itemText.length > 0) page = page.split(itemText).join(" ");
    }
    const squash = (value) => value.replace(/\s+/g, " ").trim();
    page = page.split("\n").map(squash).join("\n");
    const shortLines = new Set();
    for (const sent of (options.sentTexts || [])) {
      for (const line of String(sent).split("\n")) {
        const piece = squash(line);
        if (piece.length >= 40) page = page.split(piece).join(" ");
        else if (piece.length > 0) shortLines.add(piece);
      }
    }
    page = page.split("\n").filter((line) => !shortLines.has(line)).join("\n");
    // Панель навигации по беседе показывает каждое наше сообщение одной строкой, без
    // переводов строк, и вне элементов треда. Построчное вырезание её не узнаёт, поэтому
    // сообщения вырезаются ещё и целиком, со схлопнутыми пробелами.
    page = squash(page);
    const wholes = (options.sentTexts || []).map((sent) => squash(String(sent)));
    for (let index = 0; index < threadItems.length - 1; index++) wholes.push(squash(threadItems[index].innerText || ""));
    for (const whole of wholes) {
      if (whole.length >= 40) page = page.split(whole).join(" ");
    }
    // И каждая строка отправленного — где угодно на странице. Целиком сообщение совпадает не
    // всегда (вёрстка правит пробелы и символы), а виртуальный список к моменту проверки
    // может ещё не смонтировать беседу, и наш промпт окажется «последним сообщением».
    // Строка промпта не бывает частью плашки площадки: у запроса на конце частотность.
    for (const sent of (options.sentTexts || [])) {
      for (const line of String(sent).split("\n")) {
        const piece = squash(line);
        if (piece.length >= 8) page = page.split(piece).join(" ");
      }
    }
    return page.toLowerCase();
  };
`

// blockedStateJS возвращает причину недоступности аккаунта или пустую строку.
// Поле ввода не признак исправной страницы: заглушка Cloudflare приходит и вместо ответа.
const blockedStateJS = `(options) => {` + noticeTextJS + `
  const visible = (element) => {
    const style = window.getComputedStyle(element);
    const rect = element.getBoundingClientRect();
    return style.visibility !== "hidden" && style.display !== "none" && rect.width > 0 && rect.height > 0;
  };
  if (Array.from(document.querySelectorAll(options.blockedSelector)).some(visible)) return "challenge_or_captcha";
  const text = noticeText();
  const markers = [
    ["account_blocked", ["account has been blocked", "account is blocked", "account has been suspended", "account is suspended", "аккаунт заблокирован", "учетная запись заблокирована"]],
    ["terms_violation", ["violation of our terms", "violates our terms", "нарушение правил", "нарушение условий"]],
    ["access_restricted", ["access denied", "access restricted", "you do not have access", "доступ запрещен", "доступ запрещён", "доступ ограничен"]],
    ["challenge_or_captcha", ["one more step before you proceed", "verify you are human", "verifying you are human", "checking your browser", "unusual traffic", "проверка браузера", "подтвердите, что вы человек", "ещё один шаг"]],
  ];
  for (const [reason, phrases] of markers) {
    if (phrases.some((phrase) => text.includes(phrase))) return reason;
  }
  return "";
}`

// serverBusyJS сообщает, отказался ли DeepSeek обслуживать уже отправленное сообщение.
// Плашка отказа остаётся в треде навсегда, поэтому: есть новый ответ или кнопка остановки —
// не отказ; иначе плашка ищется только в последнем элементе треда, без треда — по всей странице.
const serverBusyJS = `(options) => {` + freshAnswerJS + noticeTextJS + `
  const phrases = ["server is busy", "сервер занят", "сервер перегружен"];
  const refused = (text) => phrases.some((phrase) => text.includes(phrase));
  if (findFreshAnswer() !== null) return false;
  if (Array.from(document.querySelectorAll(options.stopSelector)).some(visible)) return false;
  const items = Array.from(document.querySelectorAll(options.itemSelector));
  if (items.length > 0) return refused(((items[items.length - 1].innerText) || "").toLowerCase());
  return refused(noticeText());
}`

// copyLastAnswerJS нажимает кнопку копирования последнего ответа — первую в панели под
// ответом: ни aria-label, ни title у неё нет.
const copyLastAnswerJS = `(options) => {
  const answers = Array.from(document.querySelectorAll(options.answerSelector));
  const last = answers[answers.length - 1];
  if (!last) return "no_answer";
  let block = last;
  for (let i = 0; i < 3 && block.parentElement; i++) block = block.parentElement;
  const answerBottom = last.getBoundingClientRect().bottom;
  const buttons = Array.from(block.querySelectorAll('[role="button"]'))
    .filter((button) => button.getBoundingClientRect().top >= answerBottom - 4)
    .sort((left, right) => left.getBoundingClientRect().x - right.getBoundingClientRect().x);
  if (buttons.length === 0) return "no_button";
  buttons[0].click();
  return "clicked";
}`

// answerActionsReadyJS сообщает, что под последним ответом отрисована панель действий;
// панель появляется только после конца генерации.
const answerActionsReadyJS = `(options) => {
  const answers = Array.from(document.querySelectorAll(options.answerSelector));
  const last = answers[answers.length - 1];
  if (!last) return false;
  let block = last;
  for (let i = 0; i < 3 && block.parentElement; i++) block = block.parentElement;
  const answerBottom = last.getBoundingClientRect().bottom;
  return Array.from(block.querySelectorAll('[role="button"]'))
    .some((button) => button.getBoundingClientRect().top >= answerBottom - 4);
}`

// lastItemKeyJS возвращает наибольший ключ в треде или -1, если список пуст либо не виртуализирован.
const lastItemKeyJS = `(options) => {
  let max = -1;
  for (const item of Array.from(document.querySelectorAll(options.itemSelector))) {
    const key = Number(item.getAttribute(options.itemKeyAttribute));
    if (Number.isFinite(key) && key > max) max = key;
  }
  return max;
}`

// clickSendButtonJS нажимает кнопку отправки — самую правую в блоке поля ввода.
const clickSendButtonJS = `(options) => {
  const visible = (element) => {
    const style = window.getComputedStyle(element);
    const rect = element.getBoundingClientRect();
    return style.visibility !== "hidden" && style.display !== "none" && rect.width > 0 && rect.height > 0;
  };
  const composer = Array.from(document.querySelectorAll(options.composerSelector)).filter(visible)[0];
  if (!composer) return "no_composer";
  let block = composer;
  for (let i = 0; i < 4 && block.parentElement; i++) block = block.parentElement;
  const composerTop = composer.getBoundingClientRect().top;
  const buttons = Array.from(block.querySelectorAll('[role="button"], button'))
    .filter((button) => visible(button) && button.getBoundingClientRect().top >= composerTop - 4)
    .sort((left, right) => right.getBoundingClientRect().x - left.getBoundingClientRect().x);
  if (buttons.length === 0) return "no_button";
  buttons[0].click();
  return "clicked";
}`

// answerSourcesJS собирает всё, что страница может рассказать про последний ответ.
const answerSourcesJS = `(options) => {
  const visible = (element) => {
    const style = window.getComputedStyle(element);
    const rect = element.getBoundingClientRect();
    return style.visibility !== "hidden" && style.display !== "none" && rect.width > 0 && rect.height > 0;
  };
  const answers = Array.from(document.querySelectorAll(options.answerSelector)).filter(visible);
  const last = answers[answers.length - 1];
  if (!last) return {rendered: "", codeBlock: "", hasTable: false, hasHeadings: false};
  const blocks = Array.from(last.querySelectorAll("pre"))
    .map((block) => ((block.querySelector("code") || block).textContent || ""))
    .filter((text) => text.trim().length > 0);
  return {
    rendered: last.innerText || last.textContent || "",
    codeBlock: blocks.join("\n\n"),
    hasTable: last.querySelectorAll("table").length > 0,
    hasHeadings: last.querySelectorAll("h1,h2,h3,h4,h5,h6").length > 0
  };
}`

// freshAnswerJS — общая часть скриптов ожидания: ответ, появившийся после отправки, или null.
// Ориентир — ключ элемента; без виртуального списка — количество ответов.
const freshAnswerJS = `
  const visible = (element) => {
    const style = window.getComputedStyle(element);
    const rect = element.getBoundingClientRect();
    return style.visibility !== "hidden" && style.display !== "none" && rect.width > 0 && rect.height > 0;
  };
  const findFreshAnswer = () => {
    const items = Array.from(document.querySelectorAll(options.itemSelector));
    if (items.length === 0) {
      const answers = Array.from(document.querySelectorAll(options.answerSelector)).filter(visible);
      return answers.length > options.previousCount ? answers[answers.length - 1] : null;
    }
    let bestKey = options.previousKey;
    let found = null;
    for (const item of items) {
      const key = Number(item.getAttribute(options.itemKeyAttribute));
      if (!Number.isFinite(key) || key <= options.previousKey || key < bestKey) continue;
      // Последний узел, а не первый: с включённым DeepThink в сообщении два узла .ds-markdown —
      // сначала рассуждение, потом ответ. Первый 28.09.2026 переставал меняться раньше, чем
      // начинался ответ, и в структуру страницы ложилось «Теперь я дам ответ…».
      const nodes = item.querySelectorAll(options.answerSelector);
      const answer = nodes.length > 0 ? nodes[nodes.length - 1] : null;
      if (!answer) continue;
      bestKey = key;
      found = answer;
    }
    return found;
  };
`

// responseStateJS reports what the page is doing while the answer is awaited:
// generating — ответ уже появился или видна кнопка остановки, waiting — ещё ничего нет.
const responseStateJS = `(options) => {` + freshAnswerJS + `
  const stopVisible = Array.from(document.querySelectorAll(options.stopSelector)).some(visible);
  return (findFreshAnswer() !== null || stopVisible) ? "generating" : "waiting";
}`

// completedAnswerJS решает, дописан ли ответ: панель действий под ответом плюс короткая
// стабилизация, без панели — длинная. stopSelector у кнопок DeepSeek не срабатывает:
// aria-label и title у них нет.
const completedAnswerJS = `(options) => {` + freshAnswerJS + `
  const stateKey = "__seoPipelineDeepSeekResponseState";
  const answer = findFreshAnswer();
  if (!answer) return false;
  const text = (answer.innerText || answer.textContent || "").trim();
  const stopVisible = Array.from(document.querySelectorAll(options.stopSelector)).some(visible);

  let block = answer;
  for (let i = 0; i < 3 && block.parentElement; i++) block = block.parentElement;
  const answerBottom = answer.getBoundingClientRect().bottom;
  const actionsReady = Array.from(block.querySelectorAll('[role="button"]'))
    .some((button) => visible(button) && button.getBoundingClientRect().top >= answerBottom - 4);

  const now = performance.now();
  let state = window[stateKey];
  if (!state || state.element !== answer || state.text !== text || stopVisible) {
    state = {element: answer, text, changedAt: now};
    window[stateKey] = state;
    return false;
  }
  if (text.length === 0) return false;
  if (actionsReady) return now - state.changedAt >= options.settledForMs;
  return now - state.changedAt >= options.stableForMs;
}`

func isLoginURL(value string) bool {
	value = strings.ToLower(value)
	return strings.Contains(value, "/sign_in") || strings.Contains(value, "/login")
}

// modeSelector — прежняя группа выбора режима (role="radio" с data-model-type); на странице
// её больше нет, селектор ищется первым на случай возврата.
const modeSelector = `[data-model-type]`

// reasoningLabels — подписи тумблера DeepThink: своего атрибута нет, классы генерируются сборкой.
var reasoningLabels = []string{"deepthink", "глубокое мышление"}

// selectModeJS переключает интерфейс в заданный режим: data-model-type, затем тумблер
// DeepThink (expert — нажат, default — отжат), затем подпись пункта.
// Выставленный режим повторно не нажимается: второе нажатие тумблер выключит.
const selectModeJS = `(options) => {
  const visible = (element) => {
    const style = window.getComputedStyle(element);
    const rect = element.getBoundingClientRect();
    return style.visibility !== "hidden" && style.display !== "none" && rect.width > 0 && rect.height > 0;
  };
  const normalize = (value) => (value || "").replace(/\s+/g, " ").trim().toLowerCase();
  const pressed = (control) => ["aria-checked", "aria-pressed", "aria-selected"]
    .some((attribute) => control.getAttribute(attribute) === "true");
  const mode = normalize(options.mode);

  const byType = Array.from(document.querySelectorAll(options.modeSelector))
    .filter(visible)
    .find((control) => normalize(control.getAttribute("data-model-type")) === mode);
  if (byType) {
    if (pressed(byType)) return "already";
    byType.click();
    return "clicked";
  }

  if (mode === "expert" || mode === "default") {
    const reasoning = Array.from(document.querySelectorAll(options.toggleSelector))
      .filter(visible)
      .find((control) => options.reasoningLabels.includes(normalize(control.innerText)));
    if (reasoning) {
      const want = mode === "expert";
      if (pressed(reasoning) === want) return "already";
      reasoning.click();
      return "clicked";
    }
  }

  const byLabel = Array.from(document.querySelectorAll(
    'button, [role="button"], [role="menuitem"], [role="option"], [role="radio"], [role="tab"], [role="switch"]'
  )).filter(visible).find((control) => normalize(control.innerText) === mode);
  if (!byLabel) return "not_found";
  if (pressed(byLabel)) return "already";
  byLabel.click();
  return "clicked";
}`

// searchToggleSelector — тумблеры «DeepThink» и «Search» рядом с полем ввода (aria-pressed);
// различаются только подписью: своих атрибутов у кнопок нет.
const searchToggleSelector = `.ds-toggle-button, [class*="toggle-button"]`

// searchLabels — подписи тумблера поиска, обе локали.
var searchLabels = []string{"search", "поиск", "smart search", "умный поиск"}

// toggleSearchJS включает поиск, если он ещё не включён: второе нажатие его выключит.
const toggleSearchJS = `(options) => {
  const visible = (element) => {
    const style = window.getComputedStyle(element);
    const rect = element.getBoundingClientRect();
    return style.visibility !== "hidden" && style.display !== "none" && rect.width > 0 && rect.height > 0;
  };
  const normalize = (value) => (value || "").replace(/\s+/g, " ").trim().toLowerCase();
  const match = Array.from(document.querySelectorAll(options.selector))
    .filter(visible)
    .find((control) => options.labels.includes(normalize(control.innerText)));
  if (!match) return "not_found";
  if (match.getAttribute("aria-pressed") === "true") return "already";
  match.click();
  return "clicked";
}`

// attachmentsReadyJS сообщает, что карточки всех прикреплённых документов появились над
// полем ввода. Имена сверяются началом: длинное имя интерфейс обрезает многоточием.
const attachmentsReadyJS = `(options) => {
  const text = ((document.body && document.body.innerText) || "").toLowerCase();
  return options.markers.every((marker) => text.includes(marker));
}`
