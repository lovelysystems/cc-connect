package teams

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/strikethrough"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
	"golang.org/x/net/html"
)

// The agent reads the formatted copy of a message between these two lines.
const (
	htmlContextOpen  = "[Formatted original of this message (HTML)]"
	htmlContextClose = "[End of formatted original]"
)

// formatTags count as formatting the plain text lacks.
var formatTags = map[string]bool{
	"b": true, "strong": true, "i": true, "em": true, "u": true,
	"s": true, "strike": true, "del": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"ul": true, "ol": true, "li": true, "pre": true, "code": true, "table": true,
}

// formattedContext returns the message's HTML version, framed for the agent, when it carries a
// link target or formatting the plain text lacks; otherwise "". Mentions are removed and every
// other byte is kept as Teams sent it.
func (a *activity) formattedContext() string {
	body := a.htmlBody()
	if body == "" {
		return ""
	}
	kept, formatted := scanHTML(body)
	if !formatted {
		return ""
	}
	return htmlContextOpen + "\n" + strings.TrimSpace(kept) + "\n" + htmlContextClose
}

// mdConverter keeps the user's characters unescaped: its output is read by the agent and matched
// by the engine, never rendered.
var mdConverter = converter.NewConverter(
	converter.WithPlugins(
		base.NewBasePlugin(),
		commonmark.NewCommonmarkPlugin(),
		strikethrough.NewStrikethroughPlugin(),
		table.NewTablePlugin(),
	),
	converter.WithEscapeMode(converter.EscapeModeDisabled),
)

// markdown returns the message's HTML version converted to Markdown, mentions removed, when it
// carries a link target or formatting the plain text lacks; otherwise "".
func (a *activity) markdown() string {
	body := a.htmlBody()
	if body == "" {
		return ""
	}
	kept, formatted := scanHTML(body)
	if !formatted {
		return ""
	}
	md, err := mdConverter.ConvertString(kept)
	if err != nil {
		slog.Warn("teams: could not convert html to markdown", "error", err)
		return ""
	}
	md = strings.TrimSpace(md)
	if md == "" {
		slog.Warn("teams: html converted to empty markdown")
	}
	return md
}

// htmlBody returns the content of the first text/html attachment, or "".
func (a *activity) htmlBody() string {
	for _, att := range a.Attachments {
		if !strings.EqualFold(att.ContentType, "text/html") {
			continue
		}
		if len(att.Content) == 0 {
			return ""
		}
		var s string
		if err := json.Unmarshal(att.Content, &s); err != nil {
			slog.Warn("teams: unreadable html attachment", "error", err)
			return ""
		}
		return s
	}
	return ""
}

// scanHTML copies the HTML without mention elements and reports whether it carries formatting.
func scanHTML(body string) (string, bool) {
	z := html.NewTokenizer(strings.NewReader(body))
	var out strings.Builder
	formatted := false

	// Elements whose contents are dropped (a mention) or not counted (a reply quote) are
	// tracked by tag name and nesting depth.
	skipTag, skipDepth := "", 0
	replyTag, replyDepth := "", 0

	inLink := false
	var linkHref string
	var linkText strings.Builder

	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		// Raw must be copied before TagName, TagAttr or Text, which rewrite the buffer in place.
		raw := string(z.Raw())

		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			tag := string(name)
			attrs := map[string]string{}
			for hasAttr {
				var k, v []byte
				k, v, hasAttr = z.TagAttr()
				attrs[string(k)] = string(v)
			}
			opens := tt == html.StartTagToken

			if skipDepth > 0 {
				if opens && tag == skipTag {
					skipDepth++
				}
				continue
			}
			if isMention(tag, attrs) {
				if opens {
					skipTag, skipDepth = tag, 1
				}
				continue
			}
			out.WriteString(raw)

			if replyDepth > 0 {
				if opens && tag == replyTag {
					replyDepth++
				}
				continue
			}
			switch {
			case tag == "blockquote" && hasSuffixFold(attrs["itemtype"], "/Reply"):
				if opens {
					replyTag, replyDepth = tag, 1
				}
			case tag == "blockquote", formatTags[tag], hasSizeOrWeight(attrs["style"]):
				formatted = true
			case tag == "a" && opens:
				inLink, linkHref = true, attrs["href"]
				linkText.Reset()
			}

		case html.EndTagToken:
			name, _ := z.TagName()
			tag := string(name)
			if skipDepth > 0 {
				if tag == skipTag {
					skipDepth--
				}
				continue
			}
			out.WriteString(raw)
			if replyDepth > 0 {
				if tag == replyTag {
					replyDepth--
				}
				continue
			}
			if tag == "a" && inLink {
				inLink = false
				href := strings.TrimSpace(linkHref)
				if href != "" && strings.TrimSpace(linkText.String()) != href {
					formatted = true
				}
			}

		case html.TextToken:
			if skipDepth > 0 {
				continue
			}
			out.WriteString(raw)
			if inLink {
				linkText.Write(z.Text())
			}

		default:
			if skipDepth == 0 {
				out.WriteString(raw)
			}
		}
	}
	return out.String(), formatted
}

func isMention(tag string, attrs map[string]string) bool {
	return tag == "at" || hasSuffixFold(attrs["itemtype"], "schema.skype.com/Mention")
}

func hasSizeOrWeight(style string) bool {
	s := strings.ToLower(style)
	return strings.Contains(s, "font-size") || strings.Contains(s, "font-weight")
}

func hasSuffixFold(s, suffix string) bool {
	return len(s) >= len(suffix) && strings.EqualFold(s[len(s)-len(suffix):], suffix)
}
