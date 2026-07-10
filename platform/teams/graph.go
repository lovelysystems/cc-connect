package teams

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

// maxChannelFileRefs bounds how many file references the connector downloads from
// a single channel message. Each ref costs a 3-call Graph sequence and buffers up
// to maxAttachmentBytes, so an unbounded count would amplify Graph calls and
// memory; a real message attaches only a handful of files.
const maxChannelFileRefs = 10

// graphBase is the Microsoft Graph v1.0 root.
const graphBase = "https://graph.microsoft.com/v1.0"

// graphScope is the client-credentials scope for Microsoft Graph. A single token
// request targets one resource, so this is a separate token from the Bot
// Connector's (connectorScope).
const graphScope = "https://graph.microsoft.com/.default"

// newGraphTokenSource builds an app-only Graph token source, reusing the bot's
// app credentials (single-tenant). Independent of the Bot Connector token.
func newGraphTokenSource(cfg config) *oauthTokenSource {
	return newOAuthSource(cfg, tokenURL(cfg.tenantID), graphScope)
}

// channelFileRef is a downloadable file referenced by a channel message: the
// display name plus the SharePoint URL Graph returns as a `reference` attachment.
type channelFileRef struct {
	name       string
	contentURL string
}

// graphReader reads channel-message file references and downloads them. Abstracted
// for testability (dispatch injects a fake in tests).
type graphReader interface {
	// messageFileRefs reads a channel message via Graph and returns its file
	// (`reference`-type) attachments. aadGroupID is the Graph team id
	// (channelData.team.aadGroupId — NOT the thread-style teamsTeamId). When rootID
	// is non-empty and differs from msgID the message is a thread reply.
	messageFileRefs(ctx context.Context, aadGroupID, channelID, rootID, msgID string) []channelFileRef
	// downloadFile fetches a SharePoint file's bytes under Sites.Selected, bounded
	// by maxBytes. The site hosting the file must be admin-granted read.
	downloadFile(ctx context.Context, contentURL string, maxBytes int64) ([]byte, fetchOutcome)
}

// graphClient is the concrete Graph reader: an app-only Graph token + HTTP client.
type graphClient struct {
	tokens tokenSource
	http   *http.Client
	base   string // Graph API root; graphBase in production, a test server in tests
}

func newGraphClient(tokens tokenSource) *graphClient {
	return &graphClient{tokens: tokens, http: &http.Client{Timeout: connectorTimeout}, base: graphBase}
}

var _ graphReader = (*graphClient)(nil)

// graphMessage is the subset of a Graph chatMessage the connector consumes.
type graphMessage struct {
	Attachments []graphAttachment `json:"attachments"`
}

type graphAttachment struct {
	ContentType string `json:"contentType"`
	ContentURL  string `json:"contentUrl"`
	Name        string `json:"name"`
}

// messageFileRefs GETs the channel message and returns its `reference` file
// attachments. Failures (auth, transport, decode) yield no refs — never a panic;
// the caller treats an empty result as "no file".
func (g *graphClient) messageFileRefs(ctx context.Context, aadGroupID, channelID, rootID, msgID string) []channelFileRef {
	if aadGroupID == "" || channelID == "" || msgID == "" {
		return nil
	}
	// Escape every id path segment (they are opaque, never multi-segment) so a
	// malformed/unexpected id can't restructure the Graph path.
	var u string
	if rootID != "" && rootID != msgID {
		// Reply within a channel thread.
		u = fmt.Sprintf("%s/teams/%s/channels/%s/messages/%s/replies/%s",
			g.base, url.PathEscape(aadGroupID), url.PathEscape(channelID), url.PathEscape(rootID), url.PathEscape(msgID))
	} else {
		u = fmt.Sprintf("%s/teams/%s/channels/%s/messages/%s",
			g.base, url.PathEscape(aadGroupID), url.PathEscape(channelID), url.PathEscape(msgID))
	}
	body, ok := g.get(ctx, u)
	if !ok {
		return nil
	}
	var m graphMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil
	}
	var refs []channelFileRef
	for _, att := range m.Attachments {
		if strings.EqualFold(att.ContentType, "reference") && att.ContentURL != "" {
			refs = append(refs, channelFileRef{name: att.Name, contentURL: att.ContentURL})
		}
	}
	return refs
}

// site is the subset of a Graph site resource the connector consumes.
type graphSite struct {
	ID string `json:"id"`
}

// drive is the subset of a Graph drive resource the connector consumes.
type graphDrive struct {
	ID     string `json:"id"`
	WebURL string `json:"webUrl"`
}

// downloadFile resolves a SharePoint file URL to its Graph drive item and returns
// its content, under Sites.Selected. It deliberately avoids the /shares API
// (unauthorized under Sites.Selected); instead it resolves the hosting site, its
// default drive, and the item path relative to that drive, then GETs the content.
func (g *graphClient) downloadFile(ctx context.Context, contentURL string, maxBytes int64) ([]byte, fetchOutcome) {
	u, err := url.Parse(contentURL)
	if err != nil || u.Host == "" || u.Path == "" {
		return nil, fetchFailed
	}
	// 1. Resolve the hosting site from host + site-collection path (/sites/X or
	//    /teams/X); a root-hosted file has no such prefix.
	sitePath := siteCollectionPath(u.Path)
	siteRef := u.Host
	if sitePath != "" {
		siteRef = u.Host + ":" + sitePath
	}
	var site graphSite
	if !g.getJSON(ctx, fmt.Sprintf("%s/sites/%s", g.base, siteRef), &site) || site.ID == "" {
		return nil, fetchFailed
	}
	// 2. Resolve the site's default drive to learn its root folder web path
	//    (localized/renamed libraries vary, so derive it rather than assume
	//    "Shared Documents").
	var drive graphDrive
	if !g.getJSON(ctx, fmt.Sprintf("%s/sites/%s/drive", g.base, site.ID), &drive) || drive.ID == "" {
		return nil, fetchFailed
	}
	// 3. Compute the item path relative to the drive root, then GET its content.
	rel, ok := driveRelativePath(drive.WebURL, u.Path)
	if !ok {
		return nil, fetchFailed
	}
	contentURLGraph := fmt.Sprintf("%s/drives/%s/root:/%s:/content", g.base, drive.ID, rel)
	return g.getBounded(ctx, contentURLGraph, maxBytes)
}

// siteCollectionPath extracts the `/sites/X` or `/teams/X` site-collection prefix
// from a SharePoint server-relative path, or "" for a root-hosted site.
func siteCollectionPath(p string) string {
	segs := strings.Split(strings.Trim(p, "/"), "/")
	if len(segs) >= 2 && (strings.EqualFold(segs[0], "sites") || strings.EqualFold(segs[0], "teams")) {
		return "/" + segs[0] + "/" + segs[1]
	}
	return ""
}

// driveRelativePath returns the file path relative to the drive root, and
// re-encodes it for a Graph path-addressed request. driveWebURL is the drive's
// webUrl (e.g. https://host/sites/X/Shared Documents); itemPath is the file's
// decoded server-relative path.
func driveRelativePath(driveWebURL, itemPath string) (string, bool) {
	dw, err := url.Parse(driveWebURL)
	if err != nil || dw.Path == "" {
		return "", false
	}
	rel := strings.TrimPrefix(itemPath, dw.Path)
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		return "", false
	}
	// Re-encode per segment so spaces/specials survive Graph path addressing.
	parts := strings.Split(rel, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/"), true
}

// get performs an authenticated Graph GET returning the raw body (bounded by
// maxBodyBytes). ok is false on any auth/transport/status error.
func (g *graphClient) get(ctx context.Context, rawURL string) ([]byte, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, false
	}
	token, err := g.tokens.token(ctx)
	if err != nil {
		return nil, false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := g.http.Do(req)
	if err != nil {
		slog.Warn("teams: graph GET transport error", "url", rawURL, "error", err)
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Surface the common misconfig (RSC ChannelMessage.Read.Group not consented,
		// or the site not granted Sites.Selected → 403) so it isn't invisible.
		// Graph URLs carry no secret (no token in the URL).
		slog.Warn("teams: graph GET non-2xx", "status", resp.StatusCode, "url", rawURL)
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		slog.Warn("teams: graph GET read error", "url", rawURL, "error", err)
		return nil, false
	}
	return body, true
}

func (g *graphClient) getJSON(ctx context.Context, rawURL string, out any) bool {
	body, ok := g.get(ctx, rawURL)
	if !ok {
		return false
	}
	return json.Unmarshal(body, out) == nil
}

// getBounded fetches content bounded by maxBytes, following the 302 to the
// pre-authed download URL (the default client follows redirects; Go strips the
// Authorization header on the cross-host hop, which is fine — the redirect target
// is pre-authenticated). A payload over the cap is reported oversize, not truncated.
func (g *graphClient) getBounded(ctx context.Context, rawURL string, maxBytes int64) ([]byte, fetchOutcome) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fetchFailed
	}
	token, err := g.tokens.token(ctx)
	if err != nil {
		return nil, fetchFailed
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := g.http.Do(req)
	if err != nil {
		slog.Warn("teams: graph content GET transport error", "url", rawURL, "error", err)
		return nil, fetchFailed
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.Warn("teams: graph content GET non-2xx", "status", resp.StatusCode, "url", rawURL)
		return nil, fetchFailed
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		slog.Warn("teams: graph content GET read error", "url", rawURL, "error", err)
		return nil, fetchFailed
	}
	if int64(len(data)) > maxBytes {
		return nil, fetchOversize
	}
	return data, fetchOK
}
