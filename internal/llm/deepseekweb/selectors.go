package deepseekweb

import "strings"

const (
	composerSelector = `textarea#chat-input, textarea[placeholder*="DeepSeek" i], textarea[placeholder*="Message" i], [contenteditable="true"][role="textbox"]`
	answerSelector   = `[data-message-author-role="assistant"], [data-role="assistant"], .ds-markdown`
	stopSelector     = `button[aria-label*="stop" i], button[title*="stop" i], [role="button"][aria-label*="stop" i], [data-testid*="stop" i]`
	loginSelector    = `a[href*="sign_in"], a[href*="login"], form input[type="password"]`

	// fileInputSelector — поле загрузки документа рядом с полем ввода. Кнопка «Прикрепить»
	// открывает системный диалог, до которого браузеру не дотянуться, поэтому файл
	// отдаётся полю напрямую. Поле скрыто, и ждать его видимости бессмысленно.
	fileInputSelector = `input[type="file"]`

	// Тред DeepSeek — виртуальный список: смонтировано только окно сообщений, остальные
	// размонтируются при прокрутке. Поэтому число ответов на странице не растёт, и признаком
	// нового ответа служит ключ элемента: его назначает сам DeepSeek, и он монотонно растёт.
	itemKeyAttribute = "data-virtual-list-item-key"
	itemSelector     = `[data-virtual-list-item-key]`

	// blockedSelector перечисляет разметку страниц проверки и капчи. Используется только для
	// распознавания состояния — ничего не обходит и не подменяет.
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

// noticeTextJS — общая часть: текст страницы без содержимого ответов модели.
//
// Содержимое ответов вырезается, чтобы слова самой модели не принимались за состояние
// страницы: «access denied» она может написать в любой статье.
//
// Текст берётся целиком. Раньше просматривались первые 8000 символов, и это работало, пока
// проверка означала подменённую страницу: отказ в ответ на отправленное сообщение приходит
// под последним сообщением, то есть в конце длинной беседы. Окна здесь не хватит никакого —
// длина зависит от того, сколько сообщений успел отрисовать виртуальный список, — а стоит
// поиск по уже готовой строке считанных микросекунд.
const noticeTextJS = `
  const noticeText = () => {
    let page = document.body ? document.body.innerText || "" : "";
    for (const answer of Array.from(document.querySelectorAll(options.answerSelector))) {
      const answerText = answer.innerText || "";
      if (answerText.length > 0) page = page.split(answerText).join(" ");
    }
    return page.toLowerCase();
  };
`

// blockedStateJS возвращает причину недоступности аккаунта или пустую строку.
//
// Проверка Cloudflare приходит и на рабочей странице чата: сообщение уходит, а вместо ответа
// внизу появляется заглушка, при этом поле ввода остаётся на месте. Поэтому наличие поля
// ввода больше не считается признаком исправной страницы — так проверка пропускалась.
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
//
// Отказ приходит вместо ответа отдельной строкой внутри самого сообщения: «Server is busy.
// Try again later, or use Instant Mode». Узла ответа при этом не появляется, кнопки
// остановки нет, и ожидание ответа висит до конца бюджета стадии — распознать состояние
// больше нечем.
//
// Читать ради этого всю страницу нельзя, и это не теория: плашка остаётся в треде навсегда,
// а беседа у задачи одна на несколько сообщений. Страница, прочитанная целиком, отдавала
// «сервер занят» и тогда, когда ответ на новое сообщение уже писался, — стадия падала на
// первом же хартбите, и так на каждой попытке подряд (статья 17, 28.08: плашка от
// предыдущего сообщения при page_state=generating).
//
// Поэтому проверок три, и каждая закрывает свой способ обмануться:
//
//  1. появился ответ на наше сообщение — нас обслуживают, плашка чужая;
//  2. видна кнопка остановки — генерация идёт, даже если узел ответа ещё не смонтирован;
//  3. сама плашка ищется только в последнем элементе треда: DeepSeek рисует её внутри того
//     сообщения, которое отказался обслуживать, а старая остаётся выше и больше ни на что
//     не влияет.
//
// Разметку списка мы не контролируем, поэтому при её пропаже остаётся прежнее правило —
// поиск по тексту всей страницы.
const serverBusyJS = `(options) => {` + freshAnswerJS + noticeTextJS + `
  const phrases = ["server is busy", "сервер занят", "сервер перегружен"];
  const refused = (text) => phrases.some((phrase) => text.includes(phrase));
  if (findFreshAnswer() !== null) return false;
  if (Array.from(document.querySelectorAll(options.stopSelector)).some(visible)) return false;
  const items = Array.from(document.querySelectorAll(options.itemSelector));
  if (items.length > 0) return refused(((items[items.length - 1].innerText) || "").toLowerCase());
  return refused(noticeText());
}`

// copyLastAnswerJS нажимает кнопку копирования последнего ответа.
//
// Кнопка опознаётся положением, а не классом и не иконкой: панель действий стоит под
// текстом ответа, и «Копировать» в ней первая. Ни aria-label, ни title у неё нет.
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

// lastItemKeyJS возвращает наибольший ключ в треде или -1, если список пуст либо не
// виртуализирован. Значение снимается перед отправкой промпта и служит границей «нового».
const lastItemKeyJS = `(options) => {
  let max = -1;
  for (const item of Array.from(document.querySelectorAll(options.itemSelector))) {
    const key = Number(item.getAttribute(options.itemKeyAttribute));
    if (Number.isFinite(key) && key > max) max = key;
  }
  return max;
}`

// clickSendButtonJS нажимает кнопку отправки рядом с полем ввода.
//
// Как и «Копировать» под ответом, кнопка опознаётся положением: в блоке поля ввода она самая
// правая. Ни aria-label, ни title у неё нет, поэтому по атрибутам её не найти.
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

// freshAnswerJS — общая часть: возвращает элемент ответа, появившегося после отправки
// промпта, или null. Подставляется в начало скриптов ожидания, чтобы правило поиска нового
// ответа было ровно одно.
//
// Пока тред виртуализирован, ориентир — ключ элемента. Запасной путь по количеству нужен на
// случай, если разметка списка изменится: тогда поведение вернётся к прежнему.
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
      const answer = item.querySelector(options.answerSelector);
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

// completedAnswerJS решает, дописан ли ответ.
//
// Раньше единственным признаком была неизменность текста в течение нескольких секунд, а
// проверка кнопки остановки не работала: stopSelector ищет её по aria-label и title, которых
// у кнопок DeepSeek нет (см. комментарий к copyLastAnswerJS). Из-за этого любая пауза стрима
// принималась за конец ответа, статья сохранялась обрезанной, а следующий промпт уходил в
// поле ввода во время генерации — и не отправлялся.
//
// Теперь основной признак положительный: панель действий под ответом («Копировать» и
// соседние кнопки) появляется только после того, как генерация закончилась. Она ищется той
// же геометрией, что и в copyLastAnswerJS. Пока панели нет, ответ считается незавершённым и
// используется запасной признак — заметно более длинная стабилизация текста.
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

// modeSelector — переключатель режима ответа над полем ввода нового чата: группа
// role="radio" с data-model-type у каждого пункта (default — «Instant», expert, vision).
const modeSelector = `[data-model-type]`

// selectModeJS переключает интерфейс в заданный режим.
//
// Режим опознаётся сначала по data-model-type, и только потом по подписи: подпись
// локализована (в профиле стоит английский, и «Быстрый» на странице не встречается вовсе),
// а классы генерируются сборкой. Уже выбранный режим повторно не нажимается — у пунктов
// aria-checked, и второе нажатие ничего не улучшит.
const selectModeJS = `(options) => {
  const visible = (element) => {
    const style = window.getComputedStyle(element);
    const rect = element.getBoundingClientRect();
    return style.visibility !== "hidden" && style.display !== "none" && rect.width > 0 && rect.height > 0;
  };
  const normalize = (value) => (value || "").replace(/\s+/g, " ").trim().toLowerCase();
  const mode = normalize(options.mode);
  const byType = Array.from(document.querySelectorAll(options.modeSelector))
    .filter(visible)
    .find((control) => normalize(control.getAttribute("data-model-type")) === mode);
  const byLabel = () => Array.from(document.querySelectorAll(
    'button, [role="button"], [role="menuitem"], [role="option"], [role="radio"], [role="tab"], [role="switch"]'
  )).filter(visible).find((control) => normalize(control.innerText) === mode);
  const match = byType || byLabel();
  if (!match) return "not_found";
  for (const attribute of ["aria-checked", "aria-pressed", "aria-selected"]) {
    if (match.getAttribute(attribute) === "true") return "already";
  }
  match.click();
  return "clicked";
}`

// attachmentsReadyJS сообщает, что карточки всех прикреплённых документов появились над
// полем ввода. Имена сверяются началом: длинное имя интерфейс обрезает многоточием.
const attachmentsReadyJS = `(options) => {
  const text = ((document.body && document.body.innerText) || "").toLowerCase();
  return options.markers.every((marker) => text.includes(marker));
}`
