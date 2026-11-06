package teams

import (
	"context"
	"encoding/json"
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
	msg, outcome := g.readMessage(context.Background(), "group-1", "19:chan@thread.tacv2", "root-9", "msg-1")
	refs := msg.files

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
	g.readMessage(context.Background(), "group-1", "19:chan@thread.tacv2", "msg-1", "msg-1")
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
	if msg, outcome := g.readMessage(context.Background(), "g", "c", "", "m"); msg.files != nil || outcome != fetchOK {
		t.Errorf("no attachments should yield nil + fetchOK, got %+v %v", msg, outcome)
	}

	// missing ids → nil, no request, not a failed read
	if msg, outcome := g.readMessage(context.Background(), "", "c", "", "m"); msg.files != nil || outcome != fetchOK {
		t.Errorf("missing aadGroupID should yield nil + fetchOK, got %+v %v", msg, outcome)
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
		if msg, outcome := g.readMessage(context.Background(), "g", "c", "", "m"); msg.files != nil || outcome != tc.want {
			t.Errorf("%s: msg=%+v outcome=%v, want nil + %v", tc.name, msg, outcome, tc.want)
		}
		srv.Close()
	}
}

// pastedImagesServer answers every Graph read with a message whose body is html.
func pastedImagesServer(t *testing.T, html string) *httptest.Server {
	t.Helper()
	body, err := json.Marshal(map[string]any{"body": map[string]string{"contentType": "html", "content": html}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func hostedImg(src string) string {
	return `<img src="` + src + `" itemtype="http://schema.skype.com/AMSImage">`
}

func TestReadMessage_PastedImagesOnRoot(t *testing.T) {
	const teamPath = "https://graph.microsoft.com/v1.0/teams/group-1/channels/19:chan@thread.tacv2/messages/msg-1"
	srv := pastedImagesServer(t, "<p>look "+hostedImg(teamPath+"/hostedContents/aWQ9MQ==/$value")+
		hostedImg(teamPath+"/hostedContents/aWQ9Mg==/$value")+"</p>")
	g := newTestGraph(srv.URL)

	msg, outcome := g.readMessage(context.Background(), "group-1", "19:chan@thread.tacv2", "msg-1", "msg-1")

	if outcome != fetchOK {
		t.Fatalf("outcome = %v, want fetchOK", outcome)
	}
	base := srv.URL + "/teams/group-1/channels/19:chan@thread.tacv2/messages/msg-1/hostedContents/"
	want := []string{base + "aWQ9MQ==/$value", base + "aWQ9Mg==/$value"}
	if strings.Join(msg.images, " ") != strings.Join(want, " ") {
		t.Errorf("images = %q, want %q", msg.images, want)
	}
}

func TestReadMessage_PastedImageOnReply(t *testing.T) {
	srv := pastedImagesServer(t, hostedImg("https://graph.microsoft.com/v1.0/teams/group-1/channels/c/messages/root-9/replies/msg-1/hostedContents/aWQ9MQ==/$value"))
	g := newTestGraph(srv.URL)

	msg, _ := g.readMessage(context.Background(), "group-1", "c", "root-9", "msg-1")

	want := srv.URL + "/teams/group-1/channels/c/messages/root-9/replies/msg-1/hostedContents/aWQ9MQ==/$value"
	if len(msg.images) != 1 || msg.images[0] != want {
		t.Errorf("images = %q, want [%q]", msg.images, want)
	}
}

func TestReadMessage_PastedImageURLBuiltOnThisMessage(t *testing.T) {
	// A src on another host, or under another team's message, only lends its id.
	srv := pastedImagesServer(t,
		hostedImg("https://evil.example/teams/other-team/channels/x/messages/y/hostedContents/aWQ9MQ==/$value")+
			hostedImg("https://graph.microsoft.com/v1.0/teams/other-team/channels/x/messages/y/hostedContents/aWQ9Mg==/$value"))
	g := newTestGraph(srv.URL)

	msg, _ := g.readMessage(context.Background(), "group-1", "c", "msg-1", "msg-1")

	base := srv.URL + "/teams/group-1/channels/c/messages/msg-1/hostedContents/"
	want := []string{base + "aWQ9MQ==/$value", base + "aWQ9Mg==/$value"}
	if strings.Join(msg.images, " ") != strings.Join(want, " ") {
		t.Errorf("images = %q, want %q", msg.images, want)
	}
}

func TestReadMessage_PastedImageIDEscaped(t *testing.T) {
	srv := pastedImagesServer(t, hostedImg("https://graph.microsoft.com/v1.0/teams/g/channels/c/messages/m/hostedContents/a%2Fb%3Fc/$value"))
	g := newTestGraph(srv.URL)

	msg, _ := g.readMessage(context.Background(), "g", "c", "m", "m")

	want := srv.URL + "/teams/g/channels/c/messages/m/hostedContents/a%2Fb%3Fc/$value"
	if len(msg.images) != 1 || msg.images[0] != want {
		t.Errorf("images = %q, want [%q]", msg.images, want)
	}
}

func TestReadMessage_PastedImageRepeatedOnce(t *testing.T) {
	src := "https://graph.microsoft.com/v1.0/teams/g/channels/c/messages/m/hostedContents/aWQ9MQ==/$value"
	srv := pastedImagesServer(t, hostedImg(src)+hostedImg(src))
	g := newTestGraph(srv.URL)

	msg, _ := g.readMessage(context.Background(), "g", "c", "m", "m")

	if len(msg.images) != 1 {
		t.Errorf("images = %q, want one", msg.images)
	}
}

func TestReadMessage_OtherImagesIgnored(t *testing.T) {
	srv := pastedImagesServer(t, `<p>hi `+
		`<img src="https://statics.teams.cdn.office.net/evergreen-assets/personal-expressions/v2/assets/emoticons/smile/default/20_f.png" itemtype="http://schema.skype.com/Emoji">`+
		`<img src="https://media4.giphy.com/media/abc/giphy.gif">`+
		hostedImg("https://us-api.asm.skype.com/v1/objects/0-wus-d1-abc/views/imgo")+
		`<img itemtype="http://schema.skype.com/AMSImage">`+
		`</p>`)
	g := newTestGraph(srv.URL)

	msg, outcome := g.readMessage(context.Background(), "g", "c", "m", "m")

	if outcome != fetchOK || len(msg.images) != 0 {
		t.Errorf("images = %q outcome = %v, want none + fetchOK", msg.images, outcome)
	}
}

func TestReadMessage_TextBodyKeepsFiles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"body":{"contentType":"html","content":"<p>just text</p>"},"attachments":[
			{"contentType":"reference","name":"a.pdf","contentUrl":"https://ex.sharepoint.com/sites/T/Shared Documents/a.pdf"}]}`))
	}))
	defer srv.Close()
	g := newTestGraph(srv.URL)

	msg, outcome := g.readMessage(context.Background(), "g", "c", "m", "m")

	if outcome != fetchOK || len(msg.images) != 0 || len(msg.files) != 1 {
		t.Errorf("msg = %+v outcome = %v, want one file, no images", msg, outcome)
	}
}

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
