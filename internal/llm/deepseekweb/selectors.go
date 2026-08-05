package deepseekweb

import "strings"

const (
	composerSelector = `textarea#chat-input, textarea[placeholder*="DeepSeek" i], textarea[placeholder*="Message" i], [contenteditable="true"][role="textbox"]`
	answerSelector   = `[data-message-author-role="assistant"], [data-role="assistant"], .ds-markdown`
	stopSelector     = `button[aria-label*="stop" i], button[title*="stop" i], [role="button"][aria-label*="stop" i], [data-testid*="stop" i]`
	loginSelector    = `a[href*="sign_in"], a[href*="login"], form input[type="password"]`
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

// answerTextJS returns the model's own text instead of the rendered DOM.
//
// Ответ с разметкой чат показывает блоком кода, и innerText узла ответа отдаёт его вместе
// с обёртками интерфейса (ds-markdown-paragraph, пустые span). Внутри pre лежит исходный
// текст модели, поэтому берём его; если блока кода нет — обычный видимый текст.
const answerTextJS = `(element) => {
  const blocks = Array.from(element.querySelectorAll("pre"))
    .map((block) => ((block.querySelector("code") || block).textContent || ""))
    .filter((text) => text.trim().length > 0);
  if (blocks.length > 0) {
    return {source: "code_block", text: blocks.join("\n\n")};
  }
  return {source: "rendered_text", text: element.innerText || element.textContent || ""};
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
