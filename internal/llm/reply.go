package llm

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"supportflow/internal/ticket"
)

// Normalize whitespace only. Removing sentences, markup or placeholders could
// change the meaning, so those defects require a new draft instead.
func normalizeReply(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" || (len(lines) > 0 && lines[len(lines)-1] != "") {
			lines = append(lines, line)
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

var replyMarkup = regexp.MustCompile("(?m)^ *(?:#{1,6} |[-*+] |>|[0-9]+[.)] )|```|\\*\\*[^*\\n]+\\*\\*|(?:^|\\s)__[^_\\n]+__[.!?;:]?(?:\\s|$)|`[^`\\n]+`")
var replyPlaceholder = regexp.MustCompile(`(?i)\[(?:имя|фио|название|дата|сумма|номер|вставьте|укажите|ваш|клиент|name|insert|company)[^\]\n]*\]|\{\{[^}\n]+\}\}|<(?:имя|фио|name|название)[^>\n]*>`)
var replyHTML = regexp.MustCompile(`(?i)</?(?:think|analysis|final|script|div|p|br)(?:[ >\n/])|<!--`)
var replyServiceHeading = regexp.MustCompile(`(?im)^(?:категория|приоритет|резюме|анализ|category|priority|summary|reasoning|analysis|assistant|final|ответ) *:`)
var replyCitation = regexp.MustCompile(`[.!?] *\[[0-9]+\]`)
var replyPromise = regexp.MustCompile(`(?i)(?:^|[^\p{L}])(?:верн[её]м(?:ся)?|исправим|проверим|изучим|учт[её]м|передадим|сообщим|свяжемся|разбер[её]мся|найд[её]м|предоставим|восстановим|постараемся)(?:[^\p{L}]|$)`)

// These checks catch known defects, not every factual or stylistic error. The
// operator still reviews the draft. Values such as paths, error codes and names
// are preserved rather than rewritten by string replacement.
func replyIssue(text string, sources []ticket.Source) string {
	if text == "" || len(text) > 12000 || !utf8.ValidString(text) {
		return "Ответ пустой или имеет недопустимую длину."
	}
	for _, r := range text {
		if (unicode.IsControl(r) && r != '\n') || unicode.In(r, unicode.Cf) || r == utf8.RuneError {
			return "В ответе есть невидимые или повреждённые символы."
		}
	}
	if replyMarkup.MatchString(text) || replyHTML.MatchString(text) || replyCitation.MatchString(text) || strings.ContainsAny(text, "") {
		return "В ответе есть разметка или технические маркеры."
	}
	if replyPlaceholder.MatchString(text) {
		return "В ответе остались незаполненные шаблоны."
	}
	lower := strings.ToLower(text)
	if replyServiceHeading.MatchString(text) {
		return "В ответ попал заголовок служебного поля."
	}
	for _, marker := range []string{
		"требует редактирования", "черновик ответа", "вариант ответа", "ответ клиенту:",
		"для оператора", "рекомендация оператору", "перед отправкой клиенту", "как языковая модель",
		"как нейросеть", "я нейросеть", "база знаний", "базе знаний", "knowledge_articles", "draft_reply",
	} {
		if strings.Contains(lower, marker) {
			return "В ответе есть служебный комментарий вместо сообщения клиенту."
		}
	}
	if strings.HasPrefix(text, "{") || (strings.HasPrefix(text, "\"") && strings.HasSuffix(text, "\"")) ||
		(strings.HasPrefix(text, "«") && strings.HasSuffix(text, "»")) {
		return "Ответ обёрнут в JSON или внешние кавычки."
	}
	if replyPromise.MatchString(text) {
		return "В ответе есть неподтверждённое обещание будущих действий."
	}
	for _, marker := range []string{"оператор проверит", "заявка передана", "мы передали", "специалисты уже", "возврат одобрен", "деньги будут возвращены", "пожалуйста, подождите"} {
		if strings.Contains(lower, marker) {
			return "В ответе есть неподтверждённое действие или обещание."
		}
	}
	for _, source := range sources {
		if strings.Contains(lower, source.ID) {
			return "В ответ попал внутренний идентификатор статьи."
		}
	}
	seen := map[string]bool{}
	for _, paragraph := range strings.Split(text, "\n\n") {
		key := strings.ToLower(paragraph)
		if utf8.RuneCountInString(key) >= 30 && seen[key] {
			return "В ответе повторяется один и тот же абзац."
		}
		seen[key] = true
	}
	return ""
}
