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
	messageFileRefs(ctx context.Context, aadGroupID, channelID, rootID, msgID string) ([]channelFileRef, fetchOutcome)
	// resolveContentURL maps a SharePoint file URL to the Graph URL of its content,
	// under Sites.Selected. The site hosting the file must be admin-granted read.
	resolveContentURL(ctx context.Context, sharepointURL string) (string, fetchOutcome)
	// getBounded GETs a Graph content URL, bounded by maxBytes.
	getBounded(ctx context.Context, rawURL string, maxBytes int64) ([]byte, fetchOutcome)
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
// attachments. A failed GET keeps its outcome and a body that does not decode is
// fetchFailed; either yields no refs. Missing ids yield no refs and fetchOK, with
// no request.
func (g *graphClient) messageFileRefs(ctx context.Context, aadGroupID, channelID, rootID, msgID string) ([]channelFileRef, fetchOutcome) {
	if aadGroupID == "" || channelID == "" || msgID == "" {
		return nil, fetchOK
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
	var m graphMessage
	if outcome := g.getJSON(ctx, u, &m); outcome != fetchOK {
		return nil, outcome
	}
	var refs []channelFileRef
	for _, att := range m.Attachments {
		if strings.EqualFold(att.ContentType, "reference") && att.ContentURL != "" {
			refs = append(refs, channelFileRef{name: att.Name, contentURL: att.ContentURL})
		}
	}
	return refs, fetchOK
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

// resolveContentURL resolves a SharePoint file URL to the Graph content URL of its
// drive item, under Sites.Selected. It resolves the hosting site, its default
// drive, and the item path relative to that drive; the /shares API is unauthorized
// under Sites.Selected.
func (g *graphClient) resolveContentURL(ctx context.Context, sharepointURL string) (string, fetchOutcome) {
	u, err := url.Parse(sharepointURL)
	if err != nil || u.Host == "" || u.Path == "" {
		return "", fetchFailed
	}
	// 1. Resolve the hosting site from host + site-collection path (/sites/X or
	//    /teams/X); a root-hosted file has no such prefix.
	sitePath := siteCollectionPath(u.Path)
	siteRef := u.Host
	if sitePath != "" {
		siteRef = u.Host + ":" + sitePath
	}
	var site graphSite
	if outcome := g.getJSON(ctx, fmt.Sprintf("%s/sites/%s", g.base, siteRef), &site); outcome != fetchOK {
		return "", outcome
	}
	if site.ID == "" {
		return "", fetchFailed
	}
	// 2. Resolve the site's default drive to learn its root folder web path
	//    (localized/renamed libraries vary, so derive it rather than assume
	//    "Shared Documents").
	var drive graphDrive
	if outcome := g.getJSON(ctx, fmt.Sprintf("%s/sites/%s/drive", g.base, site.ID), &drive); outcome != fetchOK {
		return "", outcome
	}
	if drive.ID == "" {
		return "", fetchFailed
	}
	// 3. Compute the item path relative to the drive root.
	rel, ok := driveRelativePath(drive.WebURL, u.Path)
	if !ok {
		return "", fetchFailed
	}
	return fmt.Sprintf("%s/drives/%s/root:/%s:/content", g.base, drive.ID, rel), fetchOK
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
// maxBodyBytes). Token, transport and read errors are fetchRetryable; a non-2xx
// status is classified per statusOutcome. A 404 means a wrong site or drive, or a
// message the bot cannot read, and fails.
func (g *graphClient) get(ctx context.Context, rawURL string) ([]byte, fetchOutcome) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fetchFailed
	}
	token, err := g.tokens.token(ctx)
	if err != nil {
		return nil, fetchRetryable
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := g.http.Do(req)
	if err != nil {
		slog.Warn("teams: graph GET transport error", "url", rawURL, "error", err)
		return nil, fetchRetryable
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Surface the common misconfig (RSC ChannelMessage.Read.Group not consented,
		// or the site not granted Sites.Selected → 403) so it isn't invisible.
		// Graph URLs carry no secret (no token in the URL).
		slog.Warn("teams: graph GET non-2xx", "status", resp.StatusCode, "url", rawURL)
		return nil, statusOutcome(resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		slog.Warn("teams: graph GET read error", "url", rawURL, "error", err)
		return nil, fetchRetryable
	}
	return body, fetchOK
}

// getJSON GETs rawURL into out; a body that does not decode is fetchFailed.
func (g *graphClient) getJSON(ctx context.Context, rawURL string, out any) fetchOutcome {
	body, outcome := g.get(ctx, rawURL)
	if outcome != fetchOK {
		return outcome
	}
	if json.Unmarshal(body, out) != nil {
		return fetchFailed
	}
	return fetchOK
}

// getBounded fetches content bounded by maxBytes, following the 302 to the
// pre-authed download URL (the default client follows redirects; Go strips the
// Authorization header on the cross-host hop, which is fine — the redirect target
// is pre-authenticated). A payload over the cap is reported oversize, never
// truncated. Token, transport and read errors are fetchRetryable; a non-2xx status
// is classified per statusOutcome, with 404 retryable.
func (g *graphClient) getBounded(ctx context.Context, rawURL string, maxBytes int64) ([]byte, fetchOutcome) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fetchFailed
	}
	token, err := g.tokens.token(ctx)
	if err != nil {
		return nil, fetchRetryable
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := g.http.Do(req)
	if err != nil {
		slog.Warn("teams: graph content GET transport error", "url", rawURL, "error", err)
		return nil, fetchRetryable
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.Warn("teams: graph content GET non-2xx", "status", resp.StatusCode, "url", rawURL)
		// A file SharePoint is still saving answers 404 for a while.
		return nil, statusOutcome(resp.StatusCode, http.StatusNotFound)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		slog.Warn("teams: graph content GET read error", "url", rawURL, "error", err)
		return nil, fetchRetryable
	}
	if int64(len(data)) > maxBytes {
		return nil, fetchOversize
	}
	return data, fetchOK
}
