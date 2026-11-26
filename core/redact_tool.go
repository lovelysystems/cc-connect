package core

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	toolSecretAssignment = regexp.MustCompile(`(?i)(\b[\w-]*(?:token|secret|password|api[_-]?key|authorization|cookie|credential|bearer|session[_-]?id|access[_-]?key)[\w-]*\s*=\s*)("(?:[^"\\]|\\.)*"|'[^']*'|[^\s"'` + "`" + `&]+)`)
	toolSecretJSON       = regexp.MustCompile(`(?i)("[^"\\]*(?:token|secret|password|api[_-]?key|authorization|cookie|credential|bearer|session[_-]?id|access[_-]?key)[^"\\]*"\s*:\s*)"(?:[^"\\]|\\.)*"`)
	toolSecretHeader     = regexp.MustCompile(`(?i)((?:^|[\s"'{,(])(?:[\w-]*(?:token|secret|password|api[_-]?key|credential|cookie)[\w-]*|(?:[\w-]*[_-])?auth(?:orization|entication)?(?:[_-][\w-]*)?):[ \t]*(?:(?:Bearer|Basic|Token)\s+)?)("(?:[^"\\]|\\.)*"|'[^']*'|[^\r\n'"` + "`" + `]+)`)
	toolCurlUser         = regexp.MustCompile(`(\bcurl\b[^\n;|&]*?\s)(-u|--user)(\s+)("[^"]*"|'[^']*'|[^\s"']+)`)
	toolURLQuerySecret   = regexp.MustCompile(`(?i)([?&](?:key|sig|signature)=)[^&\s"'#` + "`" + `]+`)
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
			if toolSecretName.MatchString(key) {
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
	text = toolCurlUser.ReplaceAllStringFunc(text, redactCurlUser)
	text = toolURLQuerySecret.ReplaceAllString(text, `${1}[redacted]`)
	text = toolSecretAssignment.ReplaceAllString(text, `${1}[redacted]`)
	text = toolSecretFlag.ReplaceAllString(text, `${1}[redacted]`)
	return toolURLCredentials.ReplaceAllString(text, `${1}[redacted]@`)
}

// redactCurlUser keeps the user name of a curl `-u user:pass` argument and
// masks the password. With an empty password the user part holds the API key,
// so the user part is masked.
func redactCurlUser(match string) string {
	m := toolCurlUser.FindStringSubmatch(match)
	value := m[4]
	opening, closing := "", ""
	if value[0] == '"' || value[0] == '\'' {
		opening, closing = value[:1], value[:1]
		value = strings.Trim(value, value[:1])
	}
	user, pass, ok := strings.Cut(value, ":")
	if !ok {
		return match
	}
	if pass == "" {
		return m[1] + m[2] + m[3] + opening + "[redacted]:" + closing
	}
	return m[1] + m[2] + m[3] + opening + user + ":[redacted]" + closing
}
