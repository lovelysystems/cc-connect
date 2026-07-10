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
	refs := g.messageFileRefs(context.Background(), "group-1", "19:chan@thread.tacv2", "root-9", "msg-1")

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
	if refs := g.messageFileRefs(context.Background(), "g", "c", "", "m"); refs != nil {
		t.Errorf("no attachments should yield nil, got %+v", refs)
	}

	// 403 → nil, no panic
	deny := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer deny.Close()
	g2 := newTestGraph(deny.URL)
	if refs := g2.messageFileRefs(context.Background(), "g", "c", "", "m"); refs != nil {
		t.Errorf("403 should yield nil, got %+v", refs)
	}

	// missing ids → nil, no request
	if refs := g.messageFileRefs(context.Background(), "", "c", "", "m"); refs != nil {
		t.Errorf("missing aadGroupID should yield nil")
	}
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
	data, outcome := g.downloadFile(context.Background(),
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
	if _, outcome := g.downloadFile(context.Background(), "https://ex.sharepoint.com/sites/T/Shared Documents/a.txt", 1<<20); outcome != fetchFailed {
		t.Errorf("content 403 → %v, want fetchFailed", outcome)
	}

	// unparseable URL → failed (no request)
	if _, outcome := g.downloadFile(context.Background(), "::not a url", 1<<20); outcome != fetchFailed {
		t.Errorf("bad url → %v, want fetchFailed", outcome)
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
	if _, outcome := g.downloadFile(context.Background(), "https://ex.sharepoint.com/sites/T/Shared Documents/big.bin", 10); outcome != fetchOversize {
		t.Errorf("oversize → %v, want fetchOversize", outcome)
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
