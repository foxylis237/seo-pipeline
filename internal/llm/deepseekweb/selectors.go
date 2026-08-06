package deepseekweb

import "strings"

const (
	composerSelector = `textarea#chat-input, textarea[placeholder*="DeepSeek" i], textarea[placeholder*="Message" i], [contenteditable="true"][role="textbox"]`
	answerSelector   = `[data-message-author-role="assistant"], [data-role="assistant"], .ds-markdown`
	stopSelector     = `button[aria-label*="stop" i], button[title*="stop" i], [role="button"][aria-label*="stop" i], [data-testid*="stop" i]`
	loginSelector    = `a[href*="sign_in"], a[href*="login"], form input[type="password"]`

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

// blockedStateJS возвращает причину недоступности аккаунта или пустую строку.
//
// Пока поле ввода видно, страница считается рабочей: текст ответа модели может содержать
// любые слова, и принимать его за блокировку нельзя.
const blockedStateJS = `(options) => {
  const visible = (element) => {
    const style = window.getComputedStyle(element);
    const rect = element.getBoundingClientRect();
    return style.visibility !== "hidden" && style.display !== "none" && rect.width > 0 && rect.height > 0;
  };
  if (Array.from(document.querySelectorAll(options.composerSelector)).some(visible)) return "";
  if (Array.from(document.querySelectorAll(options.blockedSelector)).some(visible)) return "challenge_or_captcha";
  const text = (document.body ? document.body.innerText || "" : "").slice(0, 4000).toLowerCase();
  const markers = [
    ["account_blocked", ["account has been blocked", "account is blocked", "account has been suspended", "account is suspended", "аккаунт заблокирован", "учетная запись заблокирована"]],
    ["terms_violation", ["violation of our terms", "violates our terms", "нарушение правил", "нарушение условий"]],
    ["access_restricted", ["access denied", "access restricted", "you do not have access", "доступ запрещен", "доступ запрещён", "доступ ограничен"]],
    ["challenge_or_captcha", ["verify you are human", "verifying you are human", "checking your browser", "unusual traffic", "проверка браузера", "подтвердите, что вы человек"]],
  ];
  for (const [reason, phrases] of markers) {
    if (phrases.some((phrase) => text.includes(phrase))) return reason;
  }
  return "";
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

// responseStateJS reports what the page is doing while the answer is awaited:
// generating — ответ уже появился или видна кнопка остановки, waiting — ещё ничего нет.
const responseStateJS = `(options) => {
  const visible = (element) => {
    const style = window.getComputedStyle(element);
    const rect = element.getBoundingClientRect();
    return style.visibility !== "hidden" && style.display !== "none" && rect.width > 0 && rect.height > 0;
  };
  const answers = Array.from(document.querySelectorAll(options.answerSelector)).filter(visible);
  const stopVisible = Array.from(document.querySelectorAll(options.stopSelector)).some(visible);
  return (answers.length > options.previousCount || stopVisible) ? "generating" : "waiting";
}`

const completedAnswerJS = `(options) => {
  const stateKey = "__seoPipelineDeepSeekResponseState";
  const visible = (element) => {
    const style = window.getComputedStyle(element);
    const rect = element.getBoundingClientRect();
    return style.visibility !== "hidden" && style.display !== "none" && rect.width > 0 && rect.height > 0;
  };
  const answers = Array.from(document.querySelectorAll(options.answerSelector)).filter(visible);
  if (answers.length <= options.previousCount) return false;
  const answer = answers[answers.length - 1];
  const text = (answer.innerText || answer.textContent || "").trim();
  const stopVisible = Array.from(document.querySelectorAll(options.stopSelector)).some(visible);
  const now = performance.now();
  let state = window[stateKey];
  if (!state || state.element !== answer || state.text !== text || stopVisible) {
    state = {element: answer, text, changedAt: now};
    window[stateKey] = state;
    return false;
  }
  return text.length > 0 && now - state.changedAt >= options.stableForMs;
}`

func isLoginURL(value string) bool {
	value = strings.ToLower(value)
	return strings.Contains(value, "/sign_in") || strings.Contains(value, "/login")
}
