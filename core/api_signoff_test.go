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

func TestSignoffAPIRefusesARequestIDAndNonceItAlreadyAccepted(t *testing.T) {
	p := &signoffTestPlatform{}
	e := signoffAPIEngine(t, &signoffTestAgent{runAs: "claude"}, p, 100)
	api := &APIServer{engines: map[string]*Engine{"test": e}, signoffCaller: callerUnder(100)}
	body := signoffAPIBody(t, nil)
	if rec := postSignoff(api, body); rec.Code != http.StatusOK {
		t.Fatalf("first request: status %d, body %s", rec.Code, rec.Body.String())
	}
	p.waitFor(t, "card:r1")

	rec := postSignoff(api, body)
	if rec.Code != http.StatusConflict || strings.TrimSpace(rec.Body.String()) != "request_id and nonce were already used" {
		t.Fatalf("second request: status %d, body %s; want 409", rec.Code, rec.Body.String())
	}
	time.Sleep(100 * time.Millisecond)
	cards := 0
	for _, entry := range p.entries() {
		if entry == "card:r1" {
			cards++
		}
	}
	e.signoff.mu.Lock()
	pending := len(e.signoff.pending)
	e.signoff.mu.Unlock()
	if cards != 1 || pending != 0 {
		t.Fatalf("cards posted = %d, sessions with pending cards = %d; want 1, 0", cards, pending)
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

// scopedSignoffAPI serves the API routes for project a, whose agent runs as
// agent_a with pid 100, and project b, whose agent runs as agent_b with pid
// 200. It takes every request as sent by uid from under the agent callerPid.
// Only the projects in live have a live session in signoffTestKey's thread.
func scopedSignoffAPI(t *testing.T, uid, callerPid int, live ...string) (api *APIServer, pa, pb *signoffTestPlatform) {
	t.Helper()
	pa, pb = &signoffTestPlatform{}, &signoffTestPlatform{}
	a := signoffAPIEngine(t, &signoffTestAgent{runAs: "agent_a"}, pa, 100)
	a.SetRunAs("agent_a", nil)
	b := signoffAPIEngine(t, &signoffTestAgent{runAs: "agent_b"}, pb, 200)
	b.SetRunAs("agent_b", nil)
	api = &APIServer{
		engines:       map[string]*Engine{"a": a, "b": b},
		mux:           http.NewServeMux(),
		signoffCaller: callerUnder(callerPid),
		callerUID:     func(*http.Request) (int, error) { return uid, nil },
	}
	for name, e := range api.engines {
		if !slices.Contains(live, name) {
			delete(e.interactiveStates, signoffTestKey)
		}
	}
	api.SetRunAsUserIDs(map[string]int{"agent_a": testUIDAgentA, "agent_b": testUIDAgentB})
	for _, rt := range api.routes() {
		api.handle(rt)
	}
	return api, pa, pb
}

func TestSignoffAPIAnswersASessionOfAnotherProjectLikeAMissingOne(t *testing.T) {
	api, pa, pb := scopedSignoffAPI(t, testUIDAgentA, 200, "b")
	missing := serveAPI(api, http.MethodPost, "/signoff/send", bytes.NewReader(signoffAPIBody(t, func(b map[string]any) { b["session_key"] = "test:elsewhere" })))
	rec := serveAPI(api, http.MethodPost, "/signoff/send", bytes.NewReader(signoffAPIBody(t, nil)))
	if missing.Code != http.StatusNotFound || rec.Code != missing.Code || rec.Body.String() != missing.Body.String() {
		t.Fatalf("another project's session: %d %q; missing session: %d %q; want both the 404", rec.Code, rec.Body.String(), missing.Code, missing.Body.String())
	}
	time.Sleep(50 * time.Millisecond)
	if len(pa.entries()) != 0 || len(pb.entries()) != 0 {
		t.Fatalf("threads got %q and %q", pa.entries(), pb.entries())
	}
}

func TestSignoffAPIPostsForAProjectUserUnderItsAgent(t *testing.T) {
	api, pa, _ := scopedSignoffAPI(t, testUIDAgentA, 100, "a")
	rec := serveAPI(api, http.MethodPost, "/signoff/send", bytes.NewReader(signoffAPIBody(t, nil)))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"status":"posted"}` {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	pa.waitFor(t, "card:r1")
}

func TestSignoffAPIRefusesAProjectUserOutsideItsAgent(t *testing.T) {
	api, pa, _ := scopedSignoffAPI(t, testUIDAgentA, 999, "a")
	rec := serveAPI(api, http.MethodPost, "/signoff/send", bytes.NewReader(signoffAPIBody(t, nil)))
	if rec.Code != http.StatusForbidden || strings.TrimSpace(rec.Body.String()) != "caller is not under the agent of session_key" {
		t.Fatalf("status %d, body %s; want 403 from the agent check", rec.Code, rec.Body.String())
	}
	time.Sleep(50 * time.Millisecond)
	if len(pa.entries()) != 0 {
		t.Fatalf("thread got %q", pa.entries())
	}
}

func TestSignoffAPIOwnUserPostsForAnyProject(t *testing.T) {
	api, _, pb := scopedSignoffAPI(t, os.Geteuid(), 200, "b")
	rec := serveAPI(api, http.MethodPost, "/signoff/send", bytes.NewReader(signoffAPIBody(t, nil)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	pb.waitFor(t, "card:r1")
}

// --- withdraw ---

func signoffWithdrawBody(sessionKey, requestID string) *bytes.Reader {
	return bytes.NewReader([]byte(`{"session_key":"` + sessionKey + `","request_id":"` + requestID + `"}`))
}

// postedSignoffAPI is scopedSignoffAPI with card r1 open in the thread of
// project a, whose agent the caller runs under.
func postedSignoffAPI(t *testing.T, uid int) (api *APIServer, pa *signoffTestPlatform) {
	t.Helper()
	api, pa, _ = scopedSignoffAPI(t, uid, 100, "a")
	if rec := serveAPI(api, http.MethodPost, "/signoff/send", bytes.NewReader(signoffAPIBody(t, nil))); rec.Code != http.StatusOK {
		t.Fatalf("send: status %d, body %s", rec.Code, rec.Body.String())
	}
	pa.waitFor(t, "card:r1")
	return api, pa
}

func TestSignoffAPIWithdrawsACardForACallerUnderTheSessionsAgent(t *testing.T) {
	for name, uid := range map[string]int{"project user": testUIDAgentA, "own user": os.Geteuid(), "root": 0} {
		t.Run(name, func(t *testing.T) {
			api, pa := postedSignoffAPI(t, uid)
			rec := serveAPI(api, http.MethodPost, "/signoff/withdraw", signoffWithdrawBody(signoffTestKey, "r1"))
			if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"status":"withdrawn"}` {
				t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
			}
			pa.waitFor(t, "redraw:r1:replaced:")

			again := serveAPI(api, http.MethodPost, "/signoff/withdraw", signoffWithdrawBody(signoffTestKey, "r1"))
			if again.Code != http.StatusNotFound {
				t.Fatalf("second withdraw: status %d, want 404; body %s", again.Code, again.Body.String())
			}
		})
	}
}

func TestSignoffAPIWithdrawOfAnUnknownCardIsNotFound(t *testing.T) {
	api, pa := postedSignoffAPI(t, testUIDAgentA)
	rec := serveAPI(api, http.MethodPost, "/signoff/withdraw", signoffWithdrawBody(signoffTestKey, "r9"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404; body %s", rec.Code, rec.Body.String())
	}
	if pa.has("redraw:r1:replaced:") {
		t.Fatal("card r1 was withdrawn")
	}
}

func TestSignoffAPIWithdrawAnswersASessionOfAnotherProjectLikeAMissingOne(t *testing.T) {
	api, _, pb := scopedSignoffAPI(t, testUIDAgentA, 200, "b")
	b := api.engines["b"]
	target, ok := b.signoffTarget(signoffTestKey)
	if !ok || !b.acceptSignoff(target, signoffTestRequest(signoffTestKey, "r1")) {
		t.Fatal("could not post a card in project b")
	}
	pb.waitFor(t, "card:r1")

	missing := serveAPI(api, http.MethodPost, "/signoff/withdraw", signoffWithdrawBody("test:elsewhere", "r1"))
	rec := serveAPI(api, http.MethodPost, "/signoff/withdraw", signoffWithdrawBody(signoffTestKey, "r1"))
	if missing.Code != http.StatusNotFound || rec.Code != missing.Code || rec.Body.String() != missing.Body.String() {
		t.Fatalf("another project's session: %d %q; missing session: %d %q; want both the 404", rec.Code, rec.Body.String(), missing.Code, missing.Body.String())
	}
	time.Sleep(50 * time.Millisecond)
	if pb.has("redraw:r1:replaced:") {
		t.Fatal("another project's card was withdrawn")
	}
}

func TestSignoffAPIWithdrawRefusesACallerOutsideTheSessionsAgent(t *testing.T) {
	api, pa := postedSignoffAPI(t, testUIDAgentA)
	api.signoffCaller = callerUnder(999)
	rec := serveAPI(api, http.MethodPost, "/signoff/withdraw", signoffWithdrawBody(signoffTestKey, "r1"))
	if rec.Code != http.StatusForbidden || strings.TrimSpace(rec.Body.String()) != "caller is not under the agent of session_key" {
		t.Fatalf("status %d, body %s; want 403 from the agent check", rec.Code, rec.Body.String())
	}
	time.Sleep(50 * time.Millisecond)
	if pa.has("redraw:r1:replaced:") {
		t.Fatal("card r1 was withdrawn")
	}
}

func TestSignoffAPIWithdrawRefusesMalformedRequests(t *testing.T) {
	for name, body := range map[string]string{
		"missing session_key": `{"request_id":"r1"}`,
		"missing request_id":  `{"session_key":"` + signoffTestKey + `"}`,
		"unknown field":       `{"session_key":"` + signoffTestKey + `","request_id":"r1","nonce":"n"}`,
		"not JSON":            `withdraw r1`,
	} {
		t.Run(name, func(t *testing.T) {
			api, _ := postedSignoffAPI(t, testUIDAgentA)
			rec := serveAPI(api, http.MethodPost, "/signoff/withdraw", strings.NewReader(body))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400; body %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestSignoffAPIWithdrawIsPostOnly(t *testing.T) {
	api := &APIServer{engines: map[string]*Engine{}}
	rec := httptest.NewRecorder()
	api.handleSignoffWithdraw(rec, httptest.NewRequest(http.MethodGet, "/signoff/withdraw", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want 405", rec.Code)
	}
}
