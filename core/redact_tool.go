package core

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	toolSecretAssignment = regexp.MustCompile(`(?i)(\b[\w-]*(?:token|secret|password|api[_-]?key|authorization|cookie|credential|bearer|session[_-]?id|access[_-]?key)[\w-]*\s*=\s*)("(?:[^"\\]|\\.)*"|'[^']*'|[^\s"'` + "`" + `&]+)`)
	toolSecretJSON       = regexp.MustCompile(`(?i)("[^"\\]*(?:token|secret|password|api[_-]?key|authorization|cookie|credential|bearer|session[_-]?id|access[_-]?key)[^"\\]*"\s*:\s*)"(?:[^"\\]|\\.)*"`)
	toolSecretHeader     = regexp.MustCompile(`(?i)(Authorization\s*:\s*(?:Bearer|Basic|Token)\s+)("(?:[^"\\]|\\.)*"|'[^']*'|[^\s'"` + "`" + `]+)`)
	toolSecretFlag       = regexp.MustCompile(`(?i)(--[\w-]*(?:token|secret|password|api[_-]?key|credential)[\w-]*\s+)("(?:[^"\\]|\\.)*"|'[^']*'|[^\s]+)`)
	toolURLCredentials   = regexp.MustCompile(`(https?://)[^/\s@]+@`)
)

// RedactToolText removes credentials before previews are shortened. Otherwise a
// retained output tail can expose the end of a secret whose key was omitted.
func RedactToolText(text string) string {
	// Decode before applying shell/header patterns: escaped JSON quotes are not
	// the quoting delimiters of the command stored inside the JSON string.
	if json.Valid([]byte(text)) {
		decoder := json.NewDecoder(strings.NewReader(text))
		decoder.UseNumber()
		var value any
		if decoder.Decode(&value) == nil {
			value = redactToolValue(value)
			var encoded []byte
			var err error
			if strings.Contains(text, "\n") {
				encoded, err = json.MarshalIndent(value, "", "  ")
			} else {
				encoded, err = json.Marshal(value)
			}
			if err == nil {
				return string(encoded)
			}
		}
	}
	return redactPlainToolText(text)
}

var toolSecretName = regexp.MustCompile(`(?i)(token|secret|password|api[_-]?key|authorization|cookie|credential|bearer|session[_-]?id|access[_-]?key)`)

func redactToolValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		for key, field := range value {
			if _, isString := field.(string); isString && toolSecretName.MatchString(key) {
				value[key] = "[redacted]"
			} else {
				value[key] = redactToolValue(field)
			}
		}
		return value
	case []any:
		for i := range value {
			value[i] = redactToolValue(value[i])
		}
		return value
	case string:
		return RedactToolText(value)
	default:
		return value
	}
}

func redactPlainToolText(text string) string {
	text = toolSecretJSON.ReplaceAllString(text, `${1}"[redacted]"`)
	text = toolSecretHeader.ReplaceAllString(text, `${1}[redacted]`)
	text = toolSecretAssignment.ReplaceAllString(text, `${1}[redacted]`)
	text = toolSecretFlag.ReplaceAllString(text, `${1}[redacted]`)
	return toolURLCredentials.ReplaceAllString(text, `${1}[redacted]@`)
}
