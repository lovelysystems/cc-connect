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

func wantMarkdown(t *testing.T, html, want string) {
	t.Helper()
	if got := htmlActivity(t, html).markdown(); got != want {
		t.Errorf("markdown(%q) =\n%s\nwant\n%s", html, got, want)
	}
}

func wantNoMarkdown(t *testing.T, html string) {
	t.Helper()
	if got := htmlActivity(t, html).markdown(); got != "" {
		t.Errorf("markdown(%q) = %q, want none", html, got)
	}
}

func TestMarkdown_TitleConvertedLink(t *testing.T) {
	wantMarkdown(t, `<p><a href="https://example.com/x">Example Page Title</a></p>`,
		`[Example Page Title](https://example.com/x)`)
}

func TestMarkdown_LinkWithEntity(t *testing.T) {
	wantMarkdown(t, `<p><a href="https://example.com/a?b=1&amp;c=2">Page Title</a></p>`,
		`[Page Title](https://example.com/a?b=1&c=2)`)
}

func TestMarkdown_BoldItalic(t *testing.T) {
	wantMarkdown(t, `<div><div>Hello. Sending <strong>bold</strong>-<em>italic</em> rich text.</div></div>`,
		`Hello. Sending **bold**-*italic* rich text.`)
}

func TestMarkdown_MentionRemoved(t *testing.T) {
	wantMarkdown(t, `<p><span itemtype="http://schema.skype.com/Mention" itemscope itemid="1">Alice <b>Smith</b></span> please do it <b>today</b></p>`,
		`please do it **today**`)
	wantMarkdown(t, `<p><at>bot</at> please do it <b>today</b></p>`,
		`please do it **today**`)
}

func TestMarkdown_PlainMentionReply(t *testing.T) {
	wantNoMarkdown(t, `<p><span itemtype="http://schema.skype.com/Mention" itemscope itemid="0">Content Bot</span> ok, publish it</p>`)
	wantNoMarkdown(t, `<p><at>Content Bot</at> ok, publish it</p>`)
}

func TestMarkdown_TypedURL(t *testing.T) {
	wantNoMarkdown(t, `<p>see <a href="https://example.com/a?b=1&amp;c=2">https://example.com/a?b=1&amp;c=2</a></p>`)
}

func TestMarkdown_Styles(t *testing.T) {
	wantNoMarkdown(t, `<p style="font-size:18pt">Heading</p>`)
	wantNoMarkdown(t, `<p><span style="color:red">x</span></p>`)
}

func TestMarkdown_TeamsMarkupAlone(t *testing.T) {
	wantNoMarkdown(t, `<p>file <attachment id="1"></attachment></p>`)
	wantNoMarkdown(t, `<blockquote itemscope itemtype="http://schema.skype.com/Reply" itemid="9"><strong itemprop="mri">Name</strong><p itemprop="preview">text</p></blockquote><p>yes</p>`)
}

func TestMarkdown_ReplyQuoteDropped(t *testing.T) {
	wantMarkdown(t, `<blockquote itemscope itemtype="http://schema.skype.com/Reply" itemid="9"><strong itemprop="mri">Content Bot</strong><span itemprop="time" itemid="9">5. Nov.</span><p itemprop="preview">⚠️ Waiting for permission response. Reply allow / deny / allow all.</p></blockquote><p>please <b>don't</b></p>`,
		`please **don't**`)
}

func TestMarkdown_AngleBracketsAsTyped(t *testing.T) {
	wantMarkdown(t, `<p>a -&gt; b <b>neu</b></p>`, `a -> b **neu**`)
	wantMarkdown(t, `<p>Preis &lt; 5 CHF &gt; 3 <b>neu</b></p>`, `Preis < 5 CHF > 3 **neu**`)
}

func TestMarkdown_TypedEntityAsTyped(t *testing.T) {
	wantMarkdown(t, `<p>typed &amp;lt;div&amp;gt; and <b>bold</b></p>`, `typed &lt;div&gt; and **bold**`)
}

func TestMarkdown_EmojiAsText(t *testing.T) {
	wantMarkdown(t, `<p>Super <img alt="😀" src="https://statics.teams.cdn.office.net/emoji.png" itemtype="http://schema.skype.com/Emoji"> <b>danke</b></p>`,
		`Super 😀 **danke**`)
}

func TestMarkdown_EmojiAlone(t *testing.T) {
	wantMarkdown(t, `<p>what do you see? <span contenteditable="false" title="Surprised" type="(surprised)" class="animated-emoticon-20-surprised"><img itemscope itemtype="http://schema.skype.com/Emoji" itemid="surprised" src="https://statics.teams.cdn.office.net/evergreen-assets/personal-expressions/v2/assets/emoticons/surprised/default/20_f.png" title="Surprised" alt="😮" style="width:20px;height:20px"></span></p>`,
		`what do you see? 😮`)
}

func TestMarkdown_PastedImageDropped(t *testing.T) {
	wantMarkdown(t, `<p>what <strong>about</strong> this?</p><p><img src="https://at-prod.asyncgw.teams.microsoft.com/v1/objects/x/views/imgo" itemtype="http://schema.skype.com/AMSImage" alt="JB Lacroix/WireImage"></p>`,
		`what **about** this?`)
}

func TestMarkdown_Blockquote(t *testing.T) {
	wantMarkdown(t, `<blockquote>quoted text</blockquote>`, `> quoted text`)
}

func TestMarkdown_List(t *testing.T) {
	wantMarkdown(t, `<ul><li>one</li><li>two</li></ul>`, "- one\n- two")
}

func TestMarkdown_StrikethroughAndTable(t *testing.T) {
	wantMarkdown(t, `<p><s>x</s></p>`, `~~x~~`)
	got := htmlActivity(t, `<table><tr><th>a</th><th>b</th></tr><tr><td>1</td><td>2</td></tr></table>`).markdown()
	if !strings.Contains(got, "| a | b |") || !strings.Contains(got, "| 1 | 2 |") {
		t.Errorf("table = %q, want a pipe table", got)
	}
}

func TestMarkdown_CodeBlock(t *testing.T) {
	wantMarkdown(t, `<pre class="language-plaintext"><code>line one<br>line two</code></pre>`,
		"```plaintext\nline one\nline two\n```")
}

func TestMarkdown_WordPaste(t *testing.T) {
	html := `<p>Erstelle daraus eine News für radio32:</p>
<p class="MsoNormal"><span style="font-family:'Aptos';color:#1F4E79">Neue Velowege für Solothurn</span></p>
<p class="MsoNormal"><b><span style="font-size:16pt;font-family:'Aptos';color:#1F4E79">Was sich ändert</span></b></p>
<p class="MsoNormal"><span style="font-family:'Aptos'">Die Stadt baut bis Ende 2027 drei neue Velowege. Der Ausbau ist vollständig finanziert.</span></p>
<ul style="margin-top:0cm">
<li class="MsoNormal"><span style="font-family:'Aptos'">Bielstrasse bis Hauptbahnhof</span></li>
<li class="MsoNormal"><span style="font-family:'Aptos'">Aareufer bis Zuchwil</span></li>
</ul>
<p class="MsoNormal"><b><span style="font-size:16pt;font-family:'Aptos';color:#1F4E79">Wie es weitergeht</span></b></p>
<p class="MsoNormal"><span style="font-family:'Aptos'">Die Arbeiten beginnen <b>sofort</b>. Details stehen in der <a href="https://www.stadt-solothurn.ch/velo/ausbau-2027">Medienmitteilung der Stadt Solothurn</a>.</span></p>`
	want := `Erstelle daraus eine News für radio32:

Neue Velowege für Solothurn

**Was sich ändert**

Die Stadt baut bis Ende 2027 drei neue Velowege. Der Ausbau ist vollständig finanziert.

- Bielstrasse bis Hauptbahnhof
- Aareufer bis Zuchwil

**Wie es weitergeht**

Die Arbeiten beginnen **sofort**. Details stehen in der [Medienmitteilung der Stadt Solothurn](https://www.stadt-solothurn.ch/velo/ausbau-2027).`
	wantMarkdown(t, html, want)
}

func TestMarkdown_UserTextNotEscaped(t *testing.T) {
	wantMarkdown(t, `<p>rename snake_case_name per <a href="https://example.com/x">the guide</a></p>`,
		`rename snake_case_name per [the guide](https://example.com/x)`)
}

func TestMarkdown_FormattingThatConvertsToNothing(t *testing.T) {
	wantNoMarkdown(t, `<p><at>bot</at> <b> </b></p>`)
}

func TestMarkdown_NoUsableHTML(t *testing.T) {
	cases := map[string]*activity{
		"no attachments": {},
		"empty content":  {Attachments: []inboundAttachment{{ContentType: "text/html"}}},
		"non-string":     {Attachments: []inboundAttachment{{ContentType: "text/html", Content: json.RawMessage(`{"a":1}`)}}},
		"other type":     {Attachments: []inboundAttachment{{ContentType: "image/png", Content: json.RawMessage(`"<b>x</b>"`)}}},
	}
	for name, a := range cases {
		if got := a.markdown(); got != "" {
			t.Errorf("%s: got %q, want none", name, got)
		}
	}
}

func TestMarkdown_ContentTypeCaseInsensitive(t *testing.T) {
	a := htmlActivity(t, `<b>x</b>`)
	a.Attachments[0].ContentType = "Text/HTML"
	if got := a.markdown(); got != "**x**" {
		t.Errorf("got %q, want **x**", got)
	}
}
