package core

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func postAPI(t *testing.T, s *APIServer, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return serveAPI(s, http.MethodPost, path, bytes.NewReader(raw))
}

func wantStatus(t *testing.T, what string, rec *httptest.ResponseRecorder, status int, body string) {
	t.Helper()
	if rec.Code != status || strings.TrimSpace(rec.Body.String()) != body {
		t.Errorf("%s: %d %q, want %d %q", what, rec.Code, strings.TrimSpace(rec.Body.String()), status, body)
	}
}

func sentTo(s *APIServer, project string) []string {
	return s.engines[project].platforms[0].(*stubPlatformEngine).getSent()
}

func jobProjects[J any](t *testing.T, rec *httptest.ResponseRecorder, project func(J) string) []string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	var jobs []J
	if err := json.Unmarshal(rec.Body.Bytes(), &jobs); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, j := range jobs {
		out = append(out, project(j))
	}
	slices.Sort(out)
	return out
}

func TestAPIScopeSendActsOnlyForTheCallersProjects(t *testing.T) {
	s := scopeTestServer(t, testUIDAgentA)
	rec := postAPI(t, s, "/send", SendRequest{Project: "a", SessionKey: scopeTestSessionKey("a"), Message: "to a"})
	wantStatus(t, "send to a", rec, http.StatusOK, `{"status":"ok"}`)
	for _, project := range []string{"b", "d", "ghost"} {
		rec := postAPI(t, s, "/send", SendRequest{Project: project, SessionKey: scopeTestSessionKey(project), Message: "x"})
		wantStatus(t, "send to "+project, rec, http.StatusForbidden, apiForbidden)
	}
	if got := sentTo(s, "b"); len(got) != 0 {
		t.Fatalf("project b got %q", got)
	}

	shared := scopeTestServer(t, testUIDAgentB)
	for _, project := range []string{"b", "c"} {
		rec := postAPI(t, shared, "/send", SendRequest{Project: project, SessionKey: scopeTestSessionKey(project), Message: "x"})
		wantStatus(t, "agent_b send to "+project, rec, http.StatusOK, `{"status":"ok"}`)
	}

	for name, uid := range map[string]int{"root": 0, "cc-connect's own user": os.Geteuid()} {
		own := scopeTestServer(t, uid)
		rec := postAPI(t, own, "/send", SendRequest{Project: "ghost", Message: "x"})
		wantStatus(t, name+" send to an unknown project", rec, http.StatusNotFound, `project "ghost" not found`)
		rec = postAPI(t, own, "/send", SendRequest{Project: "d", SessionKey: scopeTestSessionKey("d"), Message: "x"})
		wantStatus(t, name+" send to d", rec, http.StatusOK, `{"status":"ok"}`)
	}
}

func TestAPIScopeSendWithoutProjectUsesTheCallersOnlyProject(t *testing.T) {
	s := scopeTestServer(t, testUIDAgentA)
	rec := postAPI(t, s, "/send", SendRequest{SessionKey: scopeTestSessionKey("a"), Message: "no project"})
	wantStatus(t, "agent_a", rec, http.StatusOK, `{"status":"ok"}`)
	if got := sentTo(s, "a"); !slices.Contains(got, "no project") {
		t.Fatalf("project a got %q", got)
	}

	required := "project is required (multiple projects configured)"
	rec = postAPI(t, scopeTestServer(t, testUIDAgentB), "/send", SendRequest{SessionKey: scopeTestSessionKey("b"), Message: "x"})
	wantStatus(t, "agent_b", rec, http.StatusBadRequest, required)
	rec = postAPI(t, scopeTestServer(t, os.Geteuid()), "/send", SendRequest{SessionKey: scopeTestSessionKey("a"), Message: "x"})
	wantStatus(t, "cc-connect's own user", rec, http.StatusBadRequest, required)
}

func TestAPIScopeCronAndTimerAddActOnlyForTheCallersProjects(t *testing.T) {
	cronAdd := func(project string) CronAddRequest {
		return CronAddRequest{Project: project, SessionKey: scopeTestSessionKey("a"), CronExpr: "0 6 * * *", Prompt: "p"}
	}
	timerAdd := func(project string) TimerAddRequest {
		return TimerAddRequest{Project: project, SessionKey: scopeTestSessionKey("a"), Delay: "2h", Prompt: "p"}
	}
	addedProject := func(t *testing.T, rec *httptest.ResponseRecorder) string {
		t.Helper()
		var job struct{ Project string }
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &job) != nil {
			t.Fatalf("add: %d %s", rec.Code, rec.Body.String())
		}
		return job.Project
	}

	s := scopeTestServer(t, testUIDAgentA)
	for _, project := range []string{"a", ""} {
		if got := addedProject(t, postAPI(t, s, "/cron/add", cronAdd(project))); got != "a" {
			t.Errorf("cron add %q: job in %q, want a", project, got)
		}
		if got := addedProject(t, postAPI(t, s, "/timer/add", timerAdd(project))); got != "a" {
			t.Errorf("timer add %q: job in %q, want a", project, got)
		}
	}
	for _, project := range []string{"b", "ghost"} {
		wantStatus(t, "cron add "+project, postAPI(t, s, "/cron/add", cronAdd(project)), http.StatusForbidden, apiForbidden)
		wantStatus(t, "timer add "+project, postAPI(t, s, "/timer/add", timerAdd(project)), http.StatusForbidden, apiForbidden)
	}

	shared := scopeTestServer(t, testUIDAgentB)
	required := "project is required (multiple projects configured)"
	wantStatus(t, "agent_b cron add", postAPI(t, shared, "/cron/add", cronAdd("")), http.StatusBadRequest, required)
	wantStatus(t, "agent_b timer add", postAPI(t, shared, "/timer/add", timerAdd("")), http.StatusBadRequest, required)

	own := scopeTestServer(t, os.Geteuid())
	if got := addedProject(t, postAPI(t, own, "/cron/add", cronAdd("b"))); got != "b" {
		t.Errorf("own user cron add b: job in %q", got)
	}
	if got := addedProject(t, postAPI(t, own, "/timer/add", timerAdd("b"))); got != "b" {
		t.Errorf("own user timer add b: job in %q", got)
	}
}

func TestAPIScopeCronAndTimerListShowOnlyTheCallersJobs(t *testing.T) {
	cronProject := func(j CronJob) string { return j.Project }
	timerProject := func(j TimerJob) string { return j.Project }
	for _, c := range []struct {
		name         string
		uid          int
		cron, timers []string
	}{
		{"agent_a", testUIDAgentA, []string{"a"}, []string{"a"}},
		{"agent_b", testUIDAgentB, []string{"b", "c"}, []string{"b"}},
		{"cc-connect's own user", os.Geteuid(), []string{"a", "b", "c", "d"}, []string{"a", "b"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := scopeTestServer(t, c.uid)
			if got := jobProjects(t, serveAPI(s, http.MethodGet, "/cron/list", nil), cronProject); !slices.Equal(got, c.cron) {
				t.Errorf("cron list: %v, want %v", got, c.cron)
			}
			if got := jobProjects(t, serveAPI(s, http.MethodGet, "/timer/list", nil), timerProject); !slices.Equal(got, c.timers) {
				t.Errorf("timer list: %v, want %v", got, c.timers)
			}
			if got := jobProjects(t, serveAPI(s, http.MethodGet, "/cron/list?project="+c.cron[0], nil), cronProject); !slices.Equal(got, c.cron[:1]) {
				t.Errorf("cron list of %s: %v", c.cron[0], got)
			}
		})
	}

	s := scopeTestServer(t, testUIDAgentA)
	for _, project := range []string{"b", "ghost"} {
		wantStatus(t, "cron list "+project, serveAPI(s, http.MethodGet, "/cron/list?project="+project, nil), http.StatusForbidden, apiForbidden)
		wantStatus(t, "timer list "+project, serveAPI(s, http.MethodGet, "/timer/list?project="+project, nil), http.StatusForbidden, apiForbidden)
	}
}

func TestAPIScopeJobOfAnotherProjectAnswersLikeAMissingID(t *testing.T) {
	requests := []struct {
		name string
		send func(t *testing.T, s *APIServer, id string) *httptest.ResponseRecorder
	}{
		{"cron info", func(_ *testing.T, s *APIServer, id string) *httptest.ResponseRecorder {
			return serveAPI(s, http.MethodGet, "/cron/info?id="+id, nil)
		}},
		{"cron del", func(t *testing.T, s *APIServer, id string) *httptest.ResponseRecorder {
			return postAPI(t, s, "/cron/del", map[string]string{"id": id})
		}},
		{"cron exec", func(t *testing.T, s *APIServer, id string) *httptest.ResponseRecorder {
			return postAPI(t, s, "/cron/exec", map[string]string{"id": id})
		}},
		{"cron run", func(t *testing.T, s *APIServer, id string) *httptest.ResponseRecorder {
			return postAPI(t, s, "/cron/run", map[string]string{"id": id})
		}},
		{"cron edit", func(t *testing.T, s *APIServer, id string) *httptest.ResponseRecorder {
			return postAPI(t, s, "/cron/edit", map[string]any{"id": id, "field": "prompt", "value": "changed"})
		}},
		{"timer info", func(_ *testing.T, s *APIServer, id string) *httptest.ResponseRecorder {
			return serveAPI(s, http.MethodGet, "/timer/info?id="+id, nil)
		}},
		{"timer del", func(t *testing.T, s *APIServer, id string) *httptest.ResponseRecorder {
			return postAPI(t, s, "/timer/del", map[string]string{"id": id})
		}},
	}
	for _, req := range requests {
		t.Run(req.name, func(t *testing.T) {
			foreign := "job-b"
			if strings.HasPrefix(req.name, "timer") {
				foreign = "timer-b"
			}
			missing := req.send(t, scopeTestServer(t, os.Geteuid()), foreign+"-missing")
			if missing.Code < 400 {
				t.Fatalf("missing id answered %d", missing.Code)
			}

			s := scopeTestServer(t, testUIDAgentA)
			hidden := req.send(t, s, foreign)
			if hidden.Code != missing.Code {
				t.Errorf("status %d, want %d", hidden.Code, missing.Code)
			}
			if want := strings.ReplaceAll(missing.Body.String(), foreign+"-missing", foreign); hidden.Body.String() != want {
				t.Errorf("body %q, want %q", hidden.Body.String(), want)
			}
			if s.cron.Store().Get("job-b") == nil || s.timer.Store().Get("timer-b") == nil {
				t.Error("project b lost a job")
			}
			if got := s.cron.Store().Get("job-b"); got != nil && got.Prompt != "p" {
				t.Errorf("job-b prompt changed to %q", got.Prompt)
			}

			own := "job-a"
			if strings.HasPrefix(req.name, "timer") {
				own = "timer-a"
			}
			if rec := req.send(t, s, own); rec.Code == missing.Code && strings.Contains(rec.Body.String(), "not found") {
				t.Errorf("own job: %d %q", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestAPIScopeCronEditCannotMoveAJobToAnotherProject(t *testing.T) {
	s := scopeTestServer(t, testUIDAgentA)
	rec := postAPI(t, s, "/cron/edit", map[string]any{"id": "job-a", "field": "project", "value": "b"})
	wantStatus(t, "agent_a moves job-a to b", rec, http.StatusForbidden, apiForbidden)
	if got := s.cron.Store().Get("job-a").Project; got != "a" {
		t.Fatalf("job-a is in %q, want a", got)
	}

	shared := scopeTestServer(t, testUIDAgentB)
	if rec := postAPI(t, shared, "/cron/edit", map[string]any{"id": "job-b", "field": "project", "value": "c"}); rec.Code != http.StatusOK {
		t.Fatalf("agent_b moves job-b to c: %d %s", rec.Code, rec.Body.String())
	}

	own := scopeTestServer(t, os.Geteuid())
	if rec := postAPI(t, own, "/cron/edit", map[string]any{"id": "job-a", "field": "project", "value": "b"}); rec.Code != http.StatusOK {
		t.Fatalf("own user moves job-a to b: %d %s", rec.Code, rec.Body.String())
	}
	if got := own.cron.Store().Get("job-a").Project; got != "b" {
		t.Fatalf("job-a is in %q, want b", got)
	}
}

func TestAPIScopeRelaySendNeedsFromBoundInTheChat(t *testing.T) {
	relayServer := func(t *testing.T, uid int) *APIServer {
		s := scopeTestServer(t, uid)
		s.engines["b"].agent = &resultAgent{session: newResultAgentSession("from b")}
		s.relay.SetTimeout(time.Second)
		s.relay.Bind("test", "chat-ab", map[string]string{"a": "A", "b": "B"})
		s.relay.Bind("test", "chat-bc", map[string]string{"b": "B", "c": "C"})
		return s
	}
	inAB, inBC := "test:chat-ab:user", "test:chat-bc:user"

	s := relayServer(t, testUIDAgentA)
	for _, c := range []struct {
		name string
		req  RelayRequest
	}{
		{"from another project", RelayRequest{From: "b", To: "c", SessionKey: inBC, Message: "x"}},
		{"from nobody", RelayRequest{To: "b", SessionKey: inAB, Message: "x"}},
		{"from its project in a chat it is not bound in", RelayRequest{From: "a", To: "b", SessionKey: inBC, Message: "x"}},
	} {
		wantStatus(t, c.name, postAPI(t, s, "/relay/send", c.req), http.StatusForbidden, apiForbidden)
	}
	rec := postAPI(t, s, "/relay/send", RelayRequest{From: "a", To: "b", SessionKey: inAB, Message: "hi"})
	wantStatus(t, "from a in a chat with b", rec, http.StatusOK, `{"response":"from b"}`)

	own := relayServer(t, os.Geteuid())
	rec = postAPI(t, own, "/relay/send", RelayRequest{To: "b", SessionKey: inAB, Message: "hi"})
	wantStatus(t, "own user from nobody", rec, http.StatusOK, `{"response":"from b"}`)
}
