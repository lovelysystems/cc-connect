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

// formatTags count as formatting the plain text lacks.
var formatTags = map[string]bool{
	"b": true, "strong": true, "i": true, "em": true,
	"s": true, "strike": true, "del": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"ul": true, "ol": true, "li": true, "pre": true, "code": true, "table": true,
}

var mdConverter = newMDConverter()

func newMDConverter() *converter.Converter {
	conv := converter.NewConverter(
		converter.WithPlugins(
			base.NewBasePlugin(),
			commonmark.NewCommonmarkPlugin(),
			strikethrough.NewStrikethroughPlugin(),
			table.NewTablePlugin(),
		),
		converter.WithEscapeMode(converter.EscapeModeDisabled),
	)
	conv.Register.Renderer(func(_ converter.Context, w converter.Writer, n *html.Node) converter.RenderStatus {
		switch {
		case isMention(n), isReplyQuote(n), isPastedImage(n):
			return converter.RenderSuccess
		case isEmoji(n):
			w.WriteString(attr(n, "alt"))
			return converter.RenderSuccess
		}
		return converter.RenderTryNext
	}, converter.PriorityEarly)
	// The base plugin writes < and > in text as entities, so they pass it as private-use characters.
	conv.Register.TextTransformer(func(_ converter.Context, s string) string {
		return hideAngles.Replace(s)
	}, converter.PriorityEarly)
	conv.Register.TextTransformer(func(_ converter.Context, s string) string {
		return showAngles.Replace(s)
	}, converter.PriorityLate)
	return conv
}

var (
	hideAngles = strings.NewReplacer("<", "", ">", "")
	showAngles = strings.NewReplacer("", "<", "", ">")
)

// markdown returns the message's HTML version converted to Markdown, mentions, reply quotes and
// pasted images removed, when it carries a link target or formatting the plain text lacks;
// otherwise "".
func (a *activity) markdown() string {
	body := a.htmlBody()
	if body == "" {
		return ""
	}
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		slog.Warn("teams: could not parse html", "error", err)
		return ""
	}
	if !hasFormatting(doc) {
		return ""
	}
	out, err := mdConverter.ConvertNode(doc)
	if err != nil {
		slog.Warn("teams: could not convert html to markdown", "error", err)
		return ""
	}
	md := strings.TrimSpace(string(out))
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

// hasFormatting reports whether the HTML outside mentions and reply quotes carries formatting or
// a link whose target differs from its text.
func hasFormatting(n *html.Node) bool {
	if n.Type == html.ElementNode {
		switch {
		case isMention(n), isReplyQuote(n):
			return false
		case formatTags[n.Data], n.Data == "blockquote":
			return true
		case n.Data == "a":
			href := strings.TrimSpace(attr(n, "href"))
			if href != "" && strings.TrimSpace(textContent(n)) != href {
				return true
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if hasFormatting(c) {
			return true
		}
	}
	return false
}

func isMention(n *html.Node) bool {
	return n.Type == html.ElementNode &&
		(n.Data == "at" || hasSuffixFold(attr(n, "itemtype"), "schema.skype.com/Mention"))
}

func isReplyQuote(n *html.Node) bool {
	return n.Type == html.ElementNode && n.Data == "blockquote" && hasSuffixFold(attr(n, "itemtype"), "/Reply")
}

func isEmoji(n *html.Node) bool {
	return n.Type == html.ElementNode && n.Data == "img" && hasSuffixFold(attr(n, "itemtype"), "schema.skype.com/Emoji")
}

// isPastedImage matches an image pasted into Teams; its URL needs the bot token to fetch.
func isPastedImage(n *html.Node) bool {
	return n.Type == html.ElementNode && n.Data == "img" && hasSuffixFold(attr(n, "itemtype"), "schema.skype.com/AMSImage")
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func textContent(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.WriteString(textContent(c))
	}
	return b.String()
}

func hasSuffixFold(s, suffix string) bool {
	return len(s) >= len(suffix) && strings.EqualFold(s[len(s)-len(suffix):], suffix)
}
