package teams

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/JohannesKaufmann/dom"
	"github.com/chenhg5/cc-connect/core"
	"golang.org/x/net/html"
)

// Throwaway test build: tries every known way to download images pasted inline
// into a Teams channel message, logs each attempt, and hands the images it got to
// the agent. It sends the bot and Graph tokens to the image hosts.

const probeLogMsg = "teams: image probe"

// probeBodyPrefix is how much of a non-2xx response body is logged.
const probeBodyPrefix = 300

var hostedContentURL = regexp.MustCompile(`https://graph\.microsoft\.com/[^"'\s<>]*/hostedContents/[^"'\s<>/]+/\$value`)

// probeResult is one download attempt.
type probeResult struct {
	status  int
	data    []byte
	sniffed string
}

func (r probeResult) isImage(maxBytes int64) bool {
	return r.status >= 200 && r.status < 300 &&
		strings.HasPrefix(r.sniffed, "image/") &&
		len(r.data) > 0 && int64(len(r.data)) <= maxBytes
}

// probeInlineImages runs every probe step for a channel message and returns the
// images it could download. Image n is the n-th pasted image in the activity HTML
// and the n-th hosted content in the Graph message body; the first method that
// downloads it wins.
func (p *Platform) probeInlineImages(a *activity) []core.ImageAttachment {
	ctx := context.Background()
	maxBytes := p.cfg.maxAttachmentBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxAttachmentBytes
	}
	client := &http.Client{Timeout: connectorTimeout}

	var botToken, graphToken func() string
	if c, ok := p.conn.(*connector); ok {
		botToken = probeToken(ctx, c.tokens, a.ID, "bot")
	}
	var g *graphClient
	if gc, ok := p.graph.(*graphClient); ok {
		g = gc
		graphToken = probeToken(ctx, gc.tokens, a.ID, "graph")
	}

	// Activity dump.
	for i, att := range a.Attachments {
		slog.Info(probeLogMsg, "msg_id", a.ID, "step", "activity_attachment", "index", i,
			"content_type", att.ContentType, "content_url", att.ContentURL, "name", att.Name)
	}
	body := a.htmlBody()
	slog.Info(probeLogMsg, "msg_id", a.ID, "step", "activity_html", "html", body)

	winners := map[int]probeResult{}
	winnerMethod := map[int]string{}
	try := func(n int, step, method, rawURL, token string) {
		r := probeGet(ctx, client, a.ID, step, method, rawURL, token, maxBytes)
		if _, done := winners[n]; !done && r.isImage(maxBytes) {
			winners[n] = r
			winnerMethod[n] = method
		}
	}

	// AMSImage srcs from the activity HTML, fetched three ways.
	srcs := pastedImageSrcs(body)
	total := len(srcs)
	slog.Info(probeLogMsg, "msg_id", a.ID, "step", "ams_srcs", "count", len(srcs))
	for n, src := range srcs {
		try(n, "ams_src", "no_token", src, "")
		if botToken != nil {
			try(n, "ams_src", "bot_token", src, botToken())
		} else {
			slog.Info(probeLogMsg, "msg_id", a.ID, "step", "ams_src", "method", "bot_token", "url", src, "skipped", "no bot token source")
		}
		if graphToken != nil {
			try(n, "ams_src", "graph_token", src, graphToken())
		} else {
			slog.Info(probeLogMsg, "msg_id", a.ID, "step", "ams_src", "method", "graph_token", "url", src, "skipped", "no graph token source")
		}
	}

	// Graph: the message body's hostedContents URLs and the hostedContents list.
	if g == nil {
		slog.Info(probeLogMsg, "msg_id", a.ID, "step", "graph", "skipped", "no graph client")
	} else if msgURL := graphMessageURL(g.base, a); msgURL == "" {
		slog.Info(probeLogMsg, "msg_id", a.ID, "step", "graph", "skipped", "missing team, channel or message id",
			"team", a.ChannelData.Team.AADGroupID, "channel", a.ChannelData.Channel.ID)
	} else {
		msg := probeGet(ctx, client, a.ID, "graph_message", "graph_token", msgURL, graphToken(), maxBodyBytes)
		var gm struct {
			Body struct {
				ContentType string `json:"contentType"`
				Content     string `json:"content"`
			} `json:"body"`
		}
		if msg.status >= 200 && msg.status < 300 {
			if err := json.Unmarshal(msg.data, &gm); err != nil {
				slog.Info(probeLogMsg, "msg_id", a.ID, "step", "graph_message", "decode_error", err.Error())
			}
		}
		slog.Info(probeLogMsg, "msg_id", a.ID, "step", "graph_message_body",
			"body_content_type", gm.Body.ContentType, "body_content", gm.Body.Content)
		hosted := hostedContentURLs(gm.Body.Content)
		total = max(total, len(hosted))
		slog.Info(probeLogMsg, "msg_id", a.ID, "step", "graph_hosted_urls", "count", len(hosted))
		for n, u := range hosted {
			try(n, "graph_hosted", "graph_token", u, graphToken())
		}
		list := probeGet(ctx, client, a.ID, "graph_hosted_list", "graph_token", msgURL+"/hostedContents", graphToken(), maxBodyBytes)
		slog.Info(probeLogMsg, "msg_id", a.ID, "step", "graph_hosted_list_body", "body", string(list.data))
	}

	var images []core.ImageAttachment
	for n := 0; n < total; n++ {
		r, ok := winners[n]
		if !ok {
			slog.Info(probeLogMsg, "msg_id", a.ID, "step", "winner", "image", n, "method", "none")
			continue
		}
		slog.Info(probeLogMsg, "msg_id", a.ID, "step", "winner", "image", n, "method", winnerMethod[n],
			"sniffed", r.sniffed, "bytes", len(r.data))
		images = append(images, core.ImageAttachment{
			MimeType: r.sniffed,
			Data:     r.data,
			FileName: fmt.Sprintf("inline-%d.%s", n+1, strings.TrimPrefix(r.sniffed, "image/")),
		})
	}
	return images
}

// probeToken returns a getter for a token from src, fetched once on first use.
// A failed fetch is logged and yields "".
func probeToken(ctx context.Context, src tokenSource, msgID, kind string) func() string {
	var tok string
	var fetched bool
	return func() string {
		if !fetched {
			fetched = true
			t, err := src.token(ctx)
			if err != nil {
				slog.Info(probeLogMsg, "msg_id", msgID, "step", "token", "kind", kind, "error", err.Error())
			}
			tok = t
		}
		return tok
	}
}

// probeGet GETs rawURL, with the bearer token when token is non-empty, and logs
// the attempt. The body is read up to maxBytes+1.
func probeGet(ctx context.Context, client *http.Client, msgID, step, method, rawURL, token string, maxBytes int64) probeResult {
	attrs := []any{"msg_id", msgID, "step", step, "method", method, "url", rawURL, "url_host", urlHost(rawURL), "with_token", token != ""}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		slog.Info(probeLogMsg, append(attrs, "error", err.Error())...)
		return probeResult{}
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		slog.Info(probeLogMsg, append(attrs, "error", err.Error())...)
		return probeResult{}
	}
	defer func() { _ = resp.Body.Close() }()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	r := probeResult{status: resp.StatusCode, data: data}
	if len(data) > 0 {
		r.sniffed = http.DetectContentType(data)
	}
	attrs = append(attrs,
		"status", resp.StatusCode,
		"content_type", resp.Header.Get("Content-Type"),
		"bytes", len(data),
		"oversize", int64(len(data)) > maxBytes,
		"sniffed", r.sniffed,
		"www_authenticate", resp.Header.Get("WWW-Authenticate"),
	)
	if final := resp.Request.URL.String(); final != rawURL {
		attrs = append(attrs, "final_url", final)
	}
	if readErr != nil {
		attrs = append(attrs, "read_error", readErr.Error())
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		attrs = append(attrs, "body_prefix", string(data[:min(len(data), probeBodyPrefix)]))
	}
	slog.Info(probeLogMsg, attrs...)
	return r
}

// pastedImageSrcs returns the distinct src URLs of the pasted images in body, in
// document order.
func pastedImageSrcs(body string) []string {
	if body == "" {
		return nil
	}
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		slog.Info(probeLogMsg, "step", "ams_srcs", "parse_error", err.Error())
		return nil
	}
	var srcs []string
	seen := map[string]bool{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if isPastedImage(n) {
			if src := dom.GetAttributeOr(n, "src", ""); src != "" && !seen[src] {
				seen[src] = true
				srcs = append(srcs, src)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return srcs
}

// hostedContentURLs returns the distinct hostedContents $value URLs in a Graph
// message body, in order.
func hostedContentURLs(content string) []string {
	var urls []string
	seen := map[string]bool{}
	for _, u := range hostedContentURL.FindAllString(html.UnescapeString(content), -1) {
		if !seen[u] {
			seen[u] = true
			urls = append(urls, u)
		}
	}
	return urls
}

// graphMessageURL is the Graph URL of the activity's channel message: a thread
// reply when the conversation names a root other than the activity, else a root
// message. "" when an id is missing.
func graphMessageURL(base string, a *activity) string {
	team, channel, root, msgID := a.ChannelData.Team.AADGroupID, a.ChannelData.Channel.ID, a.rootMessageID(), a.ID
	if team == "" || channel == "" || msgID == "" {
		return ""
	}
	if root != "" && root != msgID {
		return fmt.Sprintf("%s/teams/%s/channels/%s/messages/%s/replies/%s",
			base, url.PathEscape(team), url.PathEscape(channel), url.PathEscape(root), url.PathEscape(msgID))
	}
	return fmt.Sprintf("%s/teams/%s/channels/%s/messages/%s",
		base, url.PathEscape(team), url.PathEscape(channel), url.PathEscape(msgID))
}

func urlHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Host
}
