package teams

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestGraph returns a graphClient pointed at the given test server.
func newTestGraph(base string) *graphClient {
	return &graphClient{tokens: &staticTokens{value: "tok"}, http: &http.Client{}, base: base}
}

func TestGraphScope(t *testing.T) {
	if graphScope != "https://graph.microsoft.com/.default" {
		t.Errorf("graphScope = %q", graphScope)
	}
}

func TestMessageFileRefs_ReplyPathAndParse(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"attachments":[
			{"contentType":"reference","name":"images.jpeg","contentUrl":"https://ex.sharepoint.com/sites/T/Shared Documents/images.jpeg"},
			{"contentType":"text/html","content":"<p>hi</p>"}
		]}`))
	}))
	defer srv.Close()

	g := newTestGraph(srv.URL)
	refs, outcome := g.messageFileRefs(context.Background(), "group-1", "19:chan@thread.tacv2", "root-9", "msg-1")

	if outcome != fetchOK {
		t.Errorf("outcome = %v, want fetchOK", outcome)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("auth = %q", gotAuth)
	}
	// root differs from msg → reply path
	if gotPath != "/teams/group-1/channels/19:chan@thread.tacv2/messages/root-9/replies/msg-1" {
		t.Errorf("path = %q, want reply path", gotPath)
	}
	if len(refs) != 1 || refs[0].name != "images.jpeg" || !strings.Contains(refs[0].contentURL, "images.jpeg") {
		t.Fatalf("refs = %+v, want one reference (text/html filtered out)", refs)
	}
}

func TestMessageFileRefs_RootPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"attachments":[]}`))
	}))
	defer srv.Close()

	g := newTestGraph(srv.URL)
	// rootID == msgID → root message, no /replies segment
	g.messageFileRefs(context.Background(), "group-1", "19:chan@thread.tacv2", "msg-1", "msg-1")
	if gotPath != "/teams/group-1/channels/19:chan@thread.tacv2/messages/msg-1" {
		t.Errorf("path = %q, want root path", gotPath)
	}
}

func TestMessageFileRefs_NoAttachmentsAndErrors(t *testing.T) {
	// no attachments → empty
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"attachments":[]}`))
	}))
	defer srv.Close()
	g := newTestGraph(srv.URL)
	if refs, outcome := g.messageFileRefs(context.Background(), "g", "c", "", "m"); refs != nil || outcome != fetchOK {
		t.Errorf("no attachments should yield nil + fetchOK, got %+v %v", refs, outcome)
	}

	// missing ids → nil, no request, not a failed read
	if refs, outcome := g.messageFileRefs(context.Background(), "", "c", "", "m"); refs != nil || outcome != fetchOK {
		t.Errorf("missing aadGroupID should yield nil + fetchOK, got %+v %v", refs, outcome)
	}
}

func TestMessageFileRefs_ReadOutcome(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   fetchOutcome
	}{
		{"403", http.StatusForbidden, "", fetchFailed},
		{"429", http.StatusTooManyRequests, "", fetchRetryable},
		{"503", http.StatusServiceUnavailable, "", fetchRetryable},
		{"undecodable body", http.StatusOK, "not json", fetchFailed},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		g := newTestGraph(srv.URL)
		if refs, outcome := g.messageFileRefs(context.Background(), "g", "c", "", "m"); refs != nil || outcome != tc.want {
			t.Errorf("%s: refs=%+v outcome=%v, want nil + %v", tc.name, refs, outcome, tc.want)
		}
		srv.Close()
	}
}

// fetchFile runs both steps of a channel file download.
func fetchFile(g *graphClient, sharepointURL string, maxBytes int64) ([]byte, fetchOutcome) {
	graphURL, outcome := g.resolveContentURL(context.Background(), sharepointURL)
	if outcome != fetchOK {
		return nil, outcome
	}
	return g.getBounded(context.Background(), graphURL, maxBytes)
}

func TestDownloadFile_ResolvesSiteDriveContent(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, "/drive"):
			_, _ = w.Write([]byte(`{"id":"DRIVEID","webUrl":"https://ex.sharepoint.com/sites/T/Shared Documents"}`))
		case strings.Contains(r.URL.Path, "/drives/") && strings.HasSuffix(r.URL.Path, "/content"):
			_, _ = w.Write([]byte("FILEBYTES"))
		case strings.HasPrefix(r.URL.Path, "/sites/"):
			_, _ = w.Write([]byte(`{"id":"SITEID"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	g := newTestGraph(srv.URL)
	data, outcome := fetchFile(g,
		"https://ex.sharepoint.com/sites/T/Shared Documents/Test/report.docx", 1<<20)

	if outcome != fetchOK || string(data) != "FILEBYTES" {
		t.Fatalf("outcome=%v data=%q, want fetchOK + FILEBYTES", outcome, data)
	}
	// site resolve → drive → content, in order
	if len(paths) != 3 {
		t.Fatalf("expected 3 Graph calls (site, drive, content), got %v", paths)
	}
	if !strings.HasPrefix(paths[0], "/sites/ex.sharepoint.com:") {
		t.Errorf("site call = %q", paths[0])
	}
	if paths[1] != "/sites/SITEID/drive" {
		t.Errorf("drive call = %q", paths[1])
	}
	// item path relative to the drive root ("Shared Documents") → "Test/report.docx"
	if paths[2] != "/drives/DRIVEID/root:/Test/report.docx:/content" {
		t.Errorf("content call = %q", paths[2])
	}
}

func TestDownloadFile_Failures(t *testing.T) {
	// content 403 → failed
	deny := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/drive"):
			_, _ = w.Write([]byte(`{"id":"D","webUrl":"https://ex.sharepoint.com/sites/T/Shared Documents"}`))
		case strings.Contains(r.URL.Path, "/drives/"):
			w.WriteHeader(http.StatusForbidden)
		default:
			_, _ = w.Write([]byte(`{"id":"S"}`))
		}
	}))
	defer deny.Close()
	g := newTestGraph(deny.URL)
	if _, outcome := fetchFile(g, "https://ex.sharepoint.com/sites/T/Shared Documents/a.txt", 1<<20); outcome != fetchFailed {
		t.Errorf("content 403 → %v, want fetchFailed", outcome)
	}

	// unparseable URL → failed (no request)
	if _, outcome := fetchFile(g, "::not a url", 1<<20); outcome != fetchFailed {
		t.Errorf("bad url → %v, want fetchFailed", outcome)
	}
}

func TestDownloadFile_RetryableContentStatus(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/drive"):
				_, _ = w.Write([]byte(`{"id":"D","webUrl":"https://ex.sharepoint.com/sites/T/Shared Documents"}`))
			case strings.Contains(r.URL.Path, "/drives/"):
				w.WriteHeader(status)
			default:
				_, _ = w.Write([]byte(`{"id":"S"}`))
			}
		}))
		g := newTestGraph(srv.URL)
		if _, outcome := fetchFile(g, "https://ex.sharepoint.com/sites/T/Shared Documents/a.jpg", 1<<20); outcome != fetchRetryable {
			t.Errorf("content %d → %v, want fetchRetryable", status, outcome)
		}
		srv.Close()
	}
}

func TestDownloadFile_LookupStatusOutcome(t *testing.T) {
	cases := []struct {
		name   string
		step   string // path suffix that answers with status
		status int
		want   fetchOutcome
	}{
		{"site 503", "site", http.StatusServiceUnavailable, fetchRetryable},
		{"site 429", "site", http.StatusTooManyRequests, fetchRetryable},
		{"site 403", "site", http.StatusForbidden, fetchFailed},
		{"drive 429", "drive", http.StatusTooManyRequests, fetchRetryable},
		{"drive 404", "drive", http.StatusNotFound, fetchFailed},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/drive"):
				if tc.step == "drive" {
					w.WriteHeader(tc.status)
					return
				}
				_, _ = w.Write([]byte(`{"id":"D","webUrl":"https://ex.sharepoint.com/sites/T/Shared Documents"}`))
			case strings.Contains(r.URL.Path, "/drives/"):
				_, _ = w.Write([]byte("X"))
			default:
				if tc.step == "site" {
					w.WriteHeader(tc.status)
					return
				}
				_, _ = w.Write([]byte(`{"id":"S"}`))
			}
		}))
		g := newTestGraph(srv.URL)
		if _, outcome := fetchFile(g, "https://ex.sharepoint.com/sites/T/Shared Documents/a.jpg", 1<<20); outcome != tc.want {
			t.Errorf("%s: outcome = %v, want %v", tc.name, outcome, tc.want)
		}
		srv.Close()
	}
}

func TestDownloadFile_SiteNotFoundFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	g := newTestGraph(srv.URL)
	if _, outcome := fetchFile(g, "https://ex.sharepoint.com/sites/T/Shared Documents/a.jpg", 1<<20); outcome != fetchFailed {
		t.Errorf("site 404 → %v, want fetchFailed", outcome)
	}
}

func TestDownloadFile_Oversize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/drive"):
			_, _ = w.Write([]byte(`{"id":"D","webUrl":"https://ex.sharepoint.com/sites/T/Shared Documents"}`))
		case strings.Contains(r.URL.Path, "/drives/"):
			_, _ = w.Write(make([]byte, 100))
		default:
			_, _ = w.Write([]byte(`{"id":"S"}`))
		}
	}))
	defer srv.Close()
	g := newTestGraph(srv.URL)
	if _, outcome := fetchFile(g, "https://ex.sharepoint.com/sites/T/Shared Documents/big.bin", 10); outcome != fetchOversize {
		t.Errorf("oversize → %v, want fetchOversize", outcome)
	}
}

func TestGraphGet_TokenErrorRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("x"))
	}))
	defer srv.Close()
	g := &graphClient{tokens: failTokens{}, http: &http.Client{}, base: srv.URL}

	if _, outcome := g.get(context.Background(), srv.URL); outcome != fetchRetryable {
		t.Errorf("get: outcome = %v, want fetchRetryable when the token cannot be acquired", outcome)
	}
	if _, outcome := g.getBounded(context.Background(), srv.URL, 1024); outcome != fetchRetryable {
		t.Errorf("getBounded: outcome = %v, want fetchRetryable when the token cannot be acquired", outcome)
	}
}

func TestSiteCollectionPath(t *testing.T) {
	cases := map[string]string{
		"/sites/Marketing/Shared Documents/x.docx": "/sites/Marketing",
		"/teams/Eng/Shared Documents/y.png":        "/teams/Eng",
		"/personal/user/Documents/z.txt":           "",
		"/foo.txt":                                 "",
		"/":                                        "",
	}
	for in, want := range cases {
		if got := siteCollectionPath(in); got != want {
			t.Errorf("siteCollectionPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDriveRelativePath(t *testing.T) {
	// spaces re-encoded per segment
	rel, ok := driveRelativePath("https://ex.sharepoint.com/sites/T/Shared%20Documents", "/sites/T/Shared Documents/Test/my file.png")
	if !ok || rel != "Test/my%20file.png" {
		t.Errorf("rel = %q ok=%v, want Test/my%%20file.png", rel, ok)
	}
	// item at drive root
	rel2, ok2 := driveRelativePath("https://ex.sharepoint.com/sites/T/Shared Documents", "/sites/T/Shared Documents/top.txt")
	if !ok2 || rel2 != "top.txt" {
		t.Errorf("rel2 = %q ok=%v, want top.txt", rel2, ok2)
	}
	// item outside the drive path (prefix doesn't match) → whole path, still ok (best-effort)
	_, ok3 := driveRelativePath("https://ex.sharepoint.com/sites/T/Shared Documents", "/sites/OTHER/lib/x")
	if !ok3 {
		t.Error("mismatched prefix should still produce a (best-effort) path, got not-ok")
	}
}
