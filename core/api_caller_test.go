package core

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	testUIDAgentA   = 7101
	testUIDAgentB   = 7102
	testUIDStranger = 7199
)

// scopeTestSessionKey is the session key of project's live chat in
// scopeTestEngine.
func scopeTestSessionKey(project string) string {
	return "test:chat-" + project + ":user"
}

// scopeTestEngine is the engine of a project whose agent runs as user, with
// a live chat under scopeTestSessionKey.
func scopeTestEngine(t *testing.T, name, user string) *Engine {
	t.Helper()
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine(name, &stubAgent{}, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	e.SetRunAs(user, nil)
	e.interactiveStates[scopeTestSessionKey(name)] = &interactiveState{platform: p, replyCtx: "chat-" + name}
	return e
}

// scopeTestServer serves the API routes for project a (agent_a), projects b
// and c (agent_b) and project d (no run_as_user), and takes every request as
// sent by uid. Each project has a cron job job-<project>; a and b have a
// timer timer-<project>.
func scopeTestServer(t *testing.T, uid int) *APIServer {
	t.Helper()
	cronStore, err := NewCronStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	timerStore, err := NewTimerStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	timers := NewTimerScheduler(timerStore)
	t.Cleanup(timers.Stop)
	s := &APIServer{
		engines: map[string]*Engine{
			"a": scopeTestEngine(t, "a", "agent_a"),
			"b": scopeTestEngine(t, "b", "agent_b"),
			"c": scopeTestEngine(t, "c", "agent_b"),
			"d": scopeTestEngine(t, "d", ""),
		},
		cron:      NewCronScheduler(cronStore),
		timer:     timers,
		relay:     NewRelayManager(""),
		mux:       http.NewServeMux(),
		callerUID: func(*http.Request) (int, error) { return uid, nil },
	}
	for name, e := range s.engines {
		s.relay.RegisterEngine(name, e)
		if err := cronStore.Add(&CronJob{ID: "job-" + name, Project: name, SessionKey: scopeTestSessionKey(name), CronExpr: "0 6 * * *", Prompt: "p"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"a", "b"} {
		if err := timerStore.Add(&TimerJob{ID: "timer-" + name, Project: name, SessionKey: scopeTestSessionKey(name), ScheduledAt: time.Now().Add(time.Hour), Prompt: "p"}); err != nil {
			t.Fatal(err)
		}
	}
	s.SetRunAsUserIDs(map[string]int{"agent_a": testUIDAgentA, "agent_b": testUIDAgentB})
	for _, rt := range s.routes() {
		s.handle(rt)
	}
	return s
}

func serveAPI(s *APIServer, method, path string, body io.Reader) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(method, path, body))
	return rec
}

// bodyReadFlag is a request body that records whether anything read it.
type bodyReadFlag struct{ read bool }

func (b *bodyReadFlag) Read([]byte) (int, error) {
	b.read = true
	return 0, io.EOF
}

func isAPIRefusal(rec *httptest.ResponseRecorder) bool {
	return rec.Code == http.StatusForbidden && strings.TrimSpace(rec.Body.String()) == apiForbidden
}

func TestAPIRoutesStateWhoMayCallThem(t *testing.T) {
	want := map[string]routeRule{
		"/send":          routeProjectScoped,
		"/sessions":      routeOwnUser,
		"/cron/add":      routeProjectScoped,
		"/cron/list":     routeProjectScoped,
		"/cron/info":     routeProjectScoped,
		"/cron/edit":     routeProjectScoped,
		"/cron/del":      routeProjectScoped,
		"/cron/exec":     routeProjectScoped,
		"/cron/run":      routeProjectScoped,
		"/timer/add":     routeProjectScoped,
		"/timer/list":    routeProjectScoped,
		"/timer/info":    routeProjectScoped,
		"/timer/del":     routeProjectScoped,
		"/relay/send":    routeProjectScoped,
		"/relay/bind":    routeOwnUser,
		"/relay/binding": routeOwnUser,
		"/signoff/send":  routeProjectScoped,
	}
	got := map[string]routeRule{}
	for _, rt := range (&APIServer{}).routes() {
		if _, dup := got[rt.path]; dup {
			t.Fatalf("route %s is listed twice", rt.path)
		}
		got[rt.path] = rt.rule
	}
	for path, rule := range want {
		if r, ok := got[path]; !ok || r != rule {
			t.Errorf("route %s: rule %v (listed %v), want %v", path, r, ok, rule)
		}
	}
	for path := range got {
		if _, ok := want[path]; !ok {
			t.Errorf("route %s has no expected rule in this test", path)
		}
	}
}

func TestAPIOwnUserAndRootReachEveryRoute(t *testing.T) {
	for name, uid := range map[string]int{"root": 0, "cc-connect's own user": os.Geteuid()} {
		t.Run(name, func(t *testing.T) {
			s := scopeTestServer(t, uid)
			if all, _ := s.CallerProjects(uid); !all {
				t.Fatalf("uid %d does not act for every project", uid)
			}
			for _, rt := range s.routes() {
				if rec := serveAPI(s, http.MethodGet, rt.path, nil); isAPIRefusal(rec) {
					t.Errorf("%s refused uid %d", rt.path, uid)
				}
			}
		})
	}
}

func TestAPICallerOfNoProjectIsRefusedBeforeTheBodyIsRead(t *testing.T) {
	s := scopeTestServer(t, testUIDStranger)
	for _, rt := range s.routes() {
		body := &bodyReadFlag{}
		if rec := serveAPI(s, http.MethodPost, rt.path, body); !isAPIRefusal(rec) {
			t.Errorf("%s: status %d, body %q; want 403", rt.path, rec.Code, rec.Body.String())
		}
		if body.read {
			t.Errorf("%s read the body of a refused request", rt.path)
		}
	}
}

func TestAPIProjectUserIsRefusedOnlyOnOwnUserRoutes(t *testing.T) {
	s := scopeTestServer(t, testUIDAgentA)
	for _, rt := range s.routes() {
		body := &bodyReadFlag{}
		rec := serveAPI(s, http.MethodPost, rt.path, body)
		switch rt.rule {
		case routeOwnUser:
			if !isAPIRefusal(rec) || body.read {
				t.Errorf("%s: status %d, body read %v; want 403 before the body is read", rt.path, rec.Code, body.read)
			}
		default:
			if isAPIRefusal(rec) {
				t.Errorf("%s refused a project's user", rt.path)
			}
		}
	}
}

func TestAPIRouteRegisteredWithoutARuleServesOnlyOwnUser(t *testing.T) {
	for _, c := range []struct {
		name   string
		uid    int
		status int
	}{
		{"project user", testUIDAgentA, http.StatusForbidden},
		{"cc-connect's own user", os.Geteuid(), http.StatusOK},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := scopeTestServer(t, c.uid)
			s.handle(apiRoute{path: "/test/unruled", handler: func(w http.ResponseWriter, _ *http.Request) {
				apiJSON(w, http.StatusOK, map[string]string{"status": "ok"})
			}})
			if rec := serveAPI(s, http.MethodGet, "/test/unruled", nil); rec.Code != c.status {
				t.Fatalf("status %d, want %d", rec.Code, c.status)
			}
		})
	}
}

func TestAPICallerProjectsFollowTheProjectsRunAsUser(t *testing.T) {
	s := scopeTestServer(t, os.Geteuid())
	s.engines["x"] = scopeTestEngine(t, "x", "agent_unresolved")
	for _, c := range []struct {
		name string
		uid  int
		want []string
	}{
		{"one project's user", testUIDAgentA, []string{"a"}},
		{"user shared by two projects", testUIDAgentB, []string{"b", "c"}},
		{"no project's user", testUIDStranger, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			all, got := s.CallerProjects(c.uid)
			if all || !slices.Equal(got, c.want) {
				t.Fatalf("CallerProjects(%d) = %v, %v; want false, %v", c.uid, all, got, c.want)
			}
		})
	}
}

func TestAPIRunAsUserIDsAreCopiedOnSet(t *testing.T) {
	ids := map[string]int{"agent_a": testUIDAgentA}
	s := &APIServer{engines: map[string]*Engine{"a": scopeTestEngine(t, "a", "agent_a")}}
	s.SetRunAsUserIDs(ids)
	ids["agent_a"] = testUIDStranger
	if _, got := s.CallerProjects(testUIDAgentA); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("after changing the caller's map, uid %d acts for %v; want [a]", testUIDAgentA, got)
	}
}

func TestAPICallerWithoutPeerCredentialsIsOwnUser(t *testing.T) {
	uid, err := requestPeerUID(httptest.NewRequest(http.MethodGet, "/send", nil), false)
	if err != nil || uid != os.Geteuid() {
		t.Fatalf("uid %d, %v; want %d", uid, err, os.Geteuid())
	}
}

func TestAPIPeerCredentialReadErrorIsRefused(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/send", nil)
	if _, err := requestPeerUID(r, true); err == nil {
		t.Fatal("a request without a socket connection has a peer uid")
	}
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()
	r = r.WithContext(context.WithValue(r.Context(), apiConnKey{}, server))
	if _, err := requestPeerUID(r, true); err == nil {
		t.Fatal("a connection that is no unix socket has a peer uid")
	}

	s := scopeTestServer(t, 0)
	s.callerUID = func(*http.Request) (int, error) { return 0, errors.New("getsockopt failed") }
	for _, path := range []string{"/send", "/sessions"} {
		if rec := serveAPI(s, http.MethodGet, path, nil); !isAPIRefusal(rec) {
			t.Errorf("%s: status %d, want 403", path, rec.Code)
		}
	}
}

func TestAPIServerReadsTheCallerFromTheSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "ccapi")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "api.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	s := &APIServer{socketPath: sock, listener: ln, mux: http.NewServeMux(), engines: map[string]*Engine{}}
	for _, rt := range s.routes() {
		s.handle(rt)
	}
	s.Start()
	t.Cleanup(s.Stop)

	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}
	resp, err := client.Get("http://api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d, body %s; want 200 for cc-connect's own user", resp.StatusCode, body)
	}
}
