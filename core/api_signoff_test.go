package core

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// signoffAPIEngine is an engine with a live, idle agent session (pid) in
// signoffTestKey's thread.
func signoffAPIEngine(t *testing.T, agent Agent, p Platform, pid int) *Engine {
	t.Helper()
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	as := newSignoffTestSession()
	as.pid = pid
	e.interactiveStates[signoffTestKey] = &interactiveState{agentSession: as, platform: p, replyCtx: "thread-1"}
	return e
}

func signoffAPIBody(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	raw, err := json.Marshal(signoffTestRequest(signoffTestKey, "r1"))
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(body)
	}
	out, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func postSignoff(api *APIServer, body []byte) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	api.handleSignoffSend(rec, httptest.NewRequest(http.MethodPost, "/signoff/send", bytes.NewReader(body)))
	return rec
}

// callerUnder answers the caller check as if the caller ran under pid.
func callerUnder(pid int) func(*http.Request, int) bool {
	return func(_ *http.Request, agentPid int) bool { return agentPid == pid }
}

func TestSignoffAPIPostsForACallerUnderTheSessionsAgent(t *testing.T) {
	p := &signoffTestPlatform{}
	e := signoffAPIEngine(t, &signoffTestAgent{runAs: "claude"}, p, 100)
	api := &APIServer{engines: map[string]*Engine{"test": e}, signoffCaller: callerUnder(100)}

	rec := postSignoff(api, signoffAPIBody(t, nil))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"status":"posted"}` {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	p.waitFor(t, "card:r1")
}

func TestSignoffAPIRefusesACallerOutsideTheSessionsAgent(t *testing.T) {
	p := &signoffTestPlatform{}
	e := signoffAPIEngine(t, &signoffTestAgent{runAs: "claude"}, p, 100)
	api := &APIServer{engines: map[string]*Engine{"test": e}, signoffCaller: callerUnder(999)}

	if rec := postSignoff(api, signoffAPIBody(t, nil)); rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403; body %s", rec.Code, rec.Body.String())
	}
	time.Sleep(50 * time.Millisecond)
	if len(p.entries()) != 0 {
		t.Fatalf("thread got %q", p.entries())
	}
}

func TestSignoffAPIPicksTheEngineWhoseAgentIsTheCallersAncestor(t *testing.T) {
	pa, pb := &signoffTestPlatform{}, &signoffTestPlatform{}
	a := signoffAPIEngine(t, &signoffTestAgent{runAs: "claude"}, pa, 100)
	b := signoffAPIEngine(t, &signoffTestAgent{runAs: "claude"}, pb, 200)
	api := &APIServer{engines: map[string]*Engine{"a": a, "b": b}, signoffCaller: callerUnder(200)}

	if rec := postSignoff(api, signoffAPIBody(t, nil)); rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	pb.waitFor(t, "card:r1")
	if len(pa.entries()) != 0 {
		t.Fatalf("the other engine's thread got %q", pa.entries())
	}
}

func TestSignoffAPIRefusesAProjectWithoutRunAsUser(t *testing.T) {
	for name, agent := range map[string]Agent{
		"no run_as_user support": &stubAgent{},
		"run_as_user empty":      &signoffTestAgent{},
	} {
		t.Run(name, func(t *testing.T) {
			p := &signoffTestPlatform{}
			e := signoffAPIEngine(t, agent, p, 100)
			api := &APIServer{engines: map[string]*Engine{"test": e}, signoffCaller: callerUnder(100)}
			if rec := postSignoff(api, signoffAPIBody(t, nil)); rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status %d, want 422; body %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestSignoffAPIRefusesAPlatformWithoutSignoffCards(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := signoffAPIEngine(t, &signoffTestAgent{runAs: "claude"}, p, 100)
	api := &APIServer{engines: map[string]*Engine{"test": e}, signoffCaller: callerUnder(100)}
	if rec := postSignoff(api, signoffAPIBody(t, nil)); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422; body %s", rec.Code, rec.Body.String())
	}
}

func TestSignoffAPIRefusesMalformedRequests(t *testing.T) {
	cases := map[string]func(map[string]any){
		"missing nonce":       func(b map[string]any) { delete(b, "nonce") },
		"empty body":          func(b map[string]any) { b["body"] = []any{} },
		"unknown style":       func(b map[string]any) { b["body"] = []any{map[string]any{"style": "bold", "text": "x"}} },
		"colon in request_id": func(b map[string]any) { b["request_id"] = "r:1" },
		"line break in text":  func(b map[string]any) { b["body"] = []any{map[string]any{"style": "name", "text": "a\nb"}} },
		"unknown field":       func(b map[string]any) { b["eyebrow"] = "Publish" },
		"project field":       func(b map[string]any) { b["project"] = "test" },
		"button in a line": func(b map[string]any) {
			b["body"] = []any{map[string]any{"style": "name", "text": "x", "action": "signoff:x:y"}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := &signoffTestPlatform{}
			e := signoffAPIEngine(t, &signoffTestAgent{runAs: "claude"}, p, 100)
			api := &APIServer{engines: map[string]*Engine{"test": e}, signoffCaller: callerUnder(100)}
			if rec := postSignoff(api, signoffAPIBody(t, mutate)); rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400; body %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestSignoffAPIRefusesASessionKeyWithNoLiveSession(t *testing.T) {
	p := &signoffTestPlatform{}
	e := signoffAPIEngine(t, &signoffTestAgent{runAs: "claude"}, p, 100)
	api := &APIServer{engines: map[string]*Engine{"test": e}, signoffCaller: callerUnder(100)}
	body := signoffAPIBody(t, func(b map[string]any) { b["session_key"] = "test:elsewhere" })
	if rec := postSignoff(api, body); rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404; body %s", rec.Code, rec.Body.String())
	}
}

func TestSignoffAPIIsPostOnly(t *testing.T) {
	api := &APIServer{engines: map[string]*Engine{}}
	rec := httptest.NewRecorder()
	api.handleSignoffSend(rec, httptest.NewRequest(http.MethodGet, "/signoff/send", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want 405", rec.Code)
	}
}

func TestSignoffAPICallerCheckFailsClosedWithoutASocket(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/signoff/send", nil)
	if callerUnderAgent(r, 100) {
		t.Fatal("a request without a socket connection passed the caller check")
	}
}
