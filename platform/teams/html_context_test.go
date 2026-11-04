package teams

import (
	"encoding/json"
	"strings"
	"testing"
)

// htmlActivity builds an activity whose only attachment is the Teams HTML version of the message.
func htmlActivity(t *testing.T, body string) *activity {
	t.Helper()
	content, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return &activity{Attachments: []inboundAttachment{{ContentType: "text/html", Content: content}}}
}

func wantBlock(t *testing.T, got, html string) {
	t.Helper()
	want := htmlContextOpen + "\n" + html + "\n" + htmlContextClose
	if got != want {
		t.Fatalf("formattedContext =\n%s\nwant\n%s", got, want)
	}
}

func TestFormattedContext_TitleConvertedLink(t *testing.T) {
	html := `<p><a href="https://example.com/x">Example Page Title</a></p>`
	wantBlock(t, htmlActivity(t, html).formattedContext(), html)
}

func TestFormattedContext_LinkWithEntityKeptByteForByte(t *testing.T) {
	html := `<p><a href="https://example.com/a?b=1&amp;c=2">Page Title</a></p>`
	wantBlock(t, htmlActivity(t, html).formattedContext(), html)
}

func TestFormattedContext_BoldItalic(t *testing.T) {
	html := `<div><div>Hello. Sending <strong>bold</strong>-<em>italic</em> rich text.</div></div>`
	wantBlock(t, htmlActivity(t, html).formattedContext(), html)
}

func TestFormattedContext_PlainMentionReply(t *testing.T) {
	cases := []string{
		`<p><span itemtype="http://schema.skype.com/Mention" itemscope itemid="0">Content Bot</span> ok, publish it</p>`,
		`<p><at>Content Bot</at> ok, publish it</p>`,
	}
	for _, html := range cases {
		if got := htmlActivity(t, html).formattedContext(); got != "" {
			t.Errorf("plain reply %q got context %q, want none", html, got)
		}
	}
}

func TestFormattedContext_MentionRemovedFormattingKept(t *testing.T) {
	html := `<p><span itemtype="http://schema.skype.com/Mention" itemscope itemid="1">Alice <b>Smith</b></span> please do it <b>today</b></p>`
	got := htmlActivity(t, html).formattedContext()
	wantBlock(t, got, `<p> please do it <b>today</b></p>`)
}

func TestFormattedContext_TypedURLAddsNothing(t *testing.T) {
	html := `<p>see <a href="https://example.com/a?b=1&amp;c=2">https://example.com/a?b=1&amp;c=2</a></p>`
	if got := htmlActivity(t, html).formattedContext(); got != "" {
		t.Errorf("typed URL got context %q, want none", got)
	}
}

func TestFormattedContext_Styles(t *testing.T) {
	heading := `<p style="font-size:18pt">Heading</p>`
	wantBlock(t, htmlActivity(t, heading).formattedContext(), heading)

	if got := htmlActivity(t, `<p><span style="color:red">x</span></p>`).formattedContext(); got != "" {
		t.Errorf("colour-only style got context %q, want none", got)
	}
}

func TestFormattedContext_TeamsMarkupAloneAddsNothing(t *testing.T) {
	cases := []string{
		`<p>nice <img alt="😀" src="https://statics.teams.cdn.office.net/emoji.png" itemtype="http://schema.skype.com/Emoji"></p>`,
		`<p>file <attachment id="1"></attachment></p>`,
		`<blockquote itemscope itemtype="http://schema.skype.com/Reply" itemid="9"><strong itemprop="mri">Name</strong><p itemprop="preview">text</p></blockquote><p>yes</p>`,
	}
	for _, html := range cases {
		if got := htmlActivity(t, html).formattedContext(); got != "" {
			t.Errorf("%q got context %q, want none", html, got)
		}
	}
}

func TestFormattedContext_PlainBlockquote(t *testing.T) {
	html := `<blockquote>quoted text</blockquote>`
	wantBlock(t, htmlActivity(t, html).formattedContext(), html)
}

func TestFormattedContext_RawBytesSurvive(t *testing.T) {
	html := `<p style='font-weight:bold; color:#123'>A&nbsp;B <span itemtype="http://schema.skype.com/Mention" itemid="0">Bot</span>C</p>`
	got := htmlActivity(t, html).formattedContext()
	wantBlock(t, got, `<p style='font-weight:bold; color:#123'>A&nbsp;B C</p>`)
}

func TestFormattedContext_NoUsableHTML(t *testing.T) {
	cases := map[string]*activity{
		"no attachments": {},
		"empty content":  {Attachments: []inboundAttachment{{ContentType: "text/html"}}},
		"non-string":     {Attachments: []inboundAttachment{{ContentType: "text/html", Content: json.RawMessage(`{"a":1}`)}}},
		"other type":     {Attachments: []inboundAttachment{{ContentType: "image/png", Content: json.RawMessage(`"<b>x</b>"`)}}},
	}
	for name, a := range cases {
		if got := a.formattedContext(); got != "" {
			t.Errorf("%s: got context %q, want none", name, got)
		}
	}
}

func TestFormattedContext_ContentTypeCaseInsensitive(t *testing.T) {
	a := htmlActivity(t, `<b>x</b>`)
	a.Attachments[0].ContentType = "Text/HTML"
	if got := a.formattedContext(); !strings.Contains(got, "<b>x</b>") {
		t.Errorf("got %q, want the block", got)
	}
}
