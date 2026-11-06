package teams

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Throwaway test build: downloads each image pasted into a channel message twice,
// through the Bot Connector attachment URL and through the Graph hostedContents
// URL, logs and compares both, and hands one copy of each image to the agent.

const probeLogMsg = "teams: image probe"

const (
	probePathBotConnector = "bot_connector"
	probePathGraph        = "graph"
)

var hostedContentURL = regexp.MustCompile(`https://graph\.microsoft\.com/[^"'\s<>]*/hostedContents/[^"'\s<>/]+/\$value`)

// hostedContentObjectID finds the Teams object id inside a decoded hostedContents
// id such as "id=x_0-ate-d1-<hex>,type=1,url=…".
var hostedContentObjectID = regexp.MustCompile(`(?:^|,)id=[^_,]*_([^,]+)`)

// probeDownload is one copy of an image.
type probeDownload struct {
	objectID string
	source   string // contentUrl or Graph URL
	name     string
	data     []byte
	outcome  fetchOutcome
	sniffed  string
	duration time.Duration
	tried    bool
}

func (d *probeDownload) usable() bool {
	return d != nil && d.tried && d.outcome == fetchOK && strings.HasPrefix(d.sniffed, "image/")
}

// probeImage pairs the two copies of one pasted image; either may be missing.
type probeImage struct {
	objectID string
	bot      *probeDownload
	graph    *probeDownload
}

// probeInlineImages downloads the channel message's pasted images through the Bot
// Connector (the activity's image/* attachments) and through Graph (graphURLs,
// from the message body), one path after the other, and returns one copy of each
// image with its sniffed MIME type. It never fails the turn.
func (p *Platform) probeInlineImages(a *activity, graphURLs []string) (images []core.ImageAttachment) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error(probeLogMsg, "msg_id", a.ID, "panic", fmt.Sprint(r))
			images = nil
		}
	}()

	var all []*probeImage
	byID := map[string]*probeImage{}
	add := func(d *probeDownload, isGraph bool) {
		img := byID[d.objectID]
		if img == nil || d.objectID == "" {
			img = &probeImage{objectID: d.objectID}
			all = append(all, img)
			if d.objectID != "" {
				byID[d.objectID] = img
			}
		}
		if isGraph {
			img.graph = d
		} else {
			img.bot = d
		}
	}
	var botDownloads, graphDownloads []*probeDownload
	if p.conn != nil {
		for _, att := range a.Attachments {
			if !att.isImage() {
				continue
			}
			d := &probeDownload{objectID: attachmentObjectID(att.ContentURL), source: att.ContentURL, name: att.Name}
			botDownloads = append(botDownloads, d)
			add(d, false)
		}
	}
	if p.graph != nil {
		for _, u := range graphURLs {
			d := &probeDownload{objectID: hostedContentObjectIDFromURL(u), source: u}
			graphDownloads = append(graphDownloads, d)
			add(d, true)
		}
	}
	if len(all) == 0 {
		return nil
	}

	ctx := context.Background()
	maxBytes := p.cfg.maxAttachmentBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxAttachmentBytes
	}
	fetchBot := func(d *probeDownload) ([]byte, fetchOutcome) {
		return p.fetchImage(ctx, inboundAttachment{ContentURL: d.source, Name: d.name}, a.ServiceURL, maxBytes)
	}
	fetchGraph := func(d *probeDownload) ([]byte, fetchOutcome) {
		return p.graph.getBounded(ctx, d.source, maxBytes)
	}

	order := []string{probePathBotConnector, probePathGraph}
	if a.ID != "" && a.ID[len(a.ID)-1]%2 == 1 {
		order = []string{probePathGraph, probePathBotConnector}
	}
	orderAttr := strings.Join(order, ",")
	for _, path := range order {
		if path == probePathBotConnector {
			probeDownloadAll(a.ID, path, orderAttr, botDownloads, fetchBot)
		} else {
			probeDownloadAll(a.ID, path, orderAttr, graphDownloads, fetchGraph)
		}
	}

	for n, img := range all {
		logProbeCompare(a.ID, orderAttr, img)
		var pick *probeDownload
		var picked string
		switch {
		case img.bot.usable():
			pick, picked = img.bot, probePathBotConnector
		case img.graph.usable():
			pick, picked = img.graph, probePathGraph
		}
		if pick == nil {
			slog.Info(probeLogMsg, "msg_id", a.ID, "step", "handover", "object_id", img.objectID, "path", "none")
			continue
		}
		slog.Info(probeLogMsg, "msg_id", a.ID, "step", "handover", "object_id", img.objectID, "path", picked,
			"sniffed", pick.sniffed, "bytes", len(pick.data))
		images = append(images, core.ImageAttachment{
			MimeType: pick.sniffed,
			Data:     pick.data,
			FileName: fmt.Sprintf("inline-%d.%s", n+1, strings.TrimPrefix(pick.sniffed, "image/")),
		})
	}
	return images
}

// probeDownloadAll downloads each copy in turn and logs one line per copy.
func probeDownloadAll(msgID, path, order string, downloads []*probeDownload, fetch func(*probeDownload) ([]byte, fetchOutcome)) {
	deadline := time.Now().Add(retryBudget)
	for _, d := range downloads {
		start := time.Now()
		d.data, d.outcome = retryFetch(deadline, "inline image", func() ([]byte, fetchOutcome) { return fetch(d) })
		d.duration = time.Since(start)
		d.tried = true
		if len(d.data) > 0 {
			d.sniffed = http.DetectContentType(d.data)
		}
		slog.Info(probeLogMsg, "msg_id", msgID, "step", "download", "path", path, "order", order,
			"object_id", d.objectID, "outcome", outcomeName(d.outcome), "bytes", len(d.data),
			"sniffed", d.sniffed, "duration_ms", d.duration.Milliseconds())
	}
}

// logProbeCompare logs whether both copies of an image arrived and are identical.
func logProbeCompare(msgID, order string, img *probeImage) {
	attrs := []any{"msg_id", msgID, "step", "compare", "order", order, "object_id", img.objectID}
	if img.bot == nil || img.graph == nil {
		only := probePathBotConnector
		if img.bot == nil {
			only = probePathGraph
		}
		slog.Info(probeLogMsg, append(attrs, "paired", false, "only", only)...)
		return
	}
	same := img.bot.outcome == fetchOK && img.graph.outcome == fetchOK &&
		sha256.Sum256(img.bot.data) == sha256.Sum256(img.graph.data)
	slog.Info(probeLogMsg, append(attrs, "paired", true, "same_bytes", same,
		"bot_connector_outcome", outcomeName(img.bot.outcome), "graph_outcome", outcomeName(img.graph.outcome),
		"bot_connector_ms", img.bot.duration.Milliseconds(), "graph_ms", img.graph.duration.Milliseconds())...)
}

func outcomeName(o fetchOutcome) string {
	switch o {
	case fetchOK:
		return "ok"
	case fetchOversize:
		return "oversize"
	case fetchFailed:
		return "failed"
	case fetchRetryable:
		return "retryable"
	}
	return fmt.Sprintf("unknown(%d)", int(o))
}

// attachmentObjectID returns the path segment after "attachments" in a Bot
// Connector attachment URL (…/v3/attachments/<object id>/views/original), or "".
func attachmentObjectID(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	segs := strings.Split(u.Path, "/")
	for i, s := range segs[:max(len(segs)-1, 0)] {
		if s == "attachments" {
			return segs[i+1]
		}
	}
	return ""
}

// hostedContentObjectIDFromURL decodes the hostedContents id in a Graph $value URL
// and returns the Teams object id it names, or "".
func hostedContentObjectIDFromURL(rawURL string) string {
	segs := strings.Split(rawURL, "/")
	if len(segs) < 2 {
		return ""
	}
	id, err := url.PathUnescape(segs[len(segs)-2])
	if err != nil {
		return ""
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		decoded, err := enc.DecodeString(id)
		if err != nil {
			continue
		}
		if m := hostedContentObjectID.FindStringSubmatch(string(decoded)); m != nil {
			return m[1]
		}
	}
	return ""
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
