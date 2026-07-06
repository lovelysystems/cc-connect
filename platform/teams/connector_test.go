package teams

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// staticTokens is a tokenSource returning a fixed token and counting calls.
type staticTokens struct {
	value string
	calls int32
}

func (s *staticTokens) token(context.Context) (string, error) {
	atomic.AddInt32(&s.calls, 1)
	return s.value, nil
}

func TestConnectorSend_PostsToCorrectURLWithBearer(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody outboundActivity
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"posted-1"}`))
	}))
	defer srv.Close()

	c := newConnector(&staticTokens{value: "tok-abc"})
	rc := replyContext{serviceURL: srv.URL, conversationID: "conv-9"}

	id, err := c.send(context.Background(), rc, outboundActivity{Type: "message", Text: "hello"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if id != "posted-1" {
		t.Errorf("id = %q, want posted-1", id)
	}
	if gotPath != "/v3/conversations/conv-9/activities" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer tok-abc" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotBody.Type != "message" || gotBody.Text != "hello" {
		t.Errorf("body = %+v", gotBody)
	}
}

type failTokens struct{}

func (failTokens) token(context.Context) (string, error) {
	return "", context.DeadlineExceeded
}

func TestConnectorSend_TokenErrorAbortsBeforeHTTP(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newConnector(failTokens{})
	_, err := c.send(context.Background(), replyContext{serviceURL: srv.URL, conversationID: "c"}, outboundActivity{Type: "message"})
	if err == nil {
		t.Fatal("expected token error to abort send")
	}
	if hit {
		t.Error("no HTTP request should be made when the token cannot be acquired")
	}
}

func TestConnectorUpdate_PutsToActivityURL(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newConnector(&staticTokens{value: "t"})
	rc := replyContext{serviceURL: srv.URL, conversationID: "c1"}
	if err := c.update(context.Background(), rc, "act-9", outboundActivity{Type: "message", Text: "x"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %s, want PUT", gotMethod)
	}
	if gotPath != "/v3/conversations/c1/activities/act-9" {
		t.Errorf("path = %q", gotPath)
	}
}

func TestConnectorUpdate_RejectsEmptyActivityID(t *testing.T) {
	c := newConnector(&staticTokens{value: "t"})
	rc := replyContext{serviceURL: "https://s/", conversationID: "c1"}
	if err := c.update(context.Background(), rc, "", outboundActivity{Type: "message"}); err == nil {
		t.Fatal("expected error for empty activityID")
	}
}

func TestConnectorSend_ErrorsOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`denied`))
	}))
	defer srv.Close()

	c := newConnector(&staticTokens{value: "t"})
	_, err := c.send(context.Background(), replyContext{serviceURL: srv.URL, conversationID: "c"}, outboundActivity{Type: "message"})
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("expected 403 error, got %v", err)
	}
}

func TestConnectorSend_MissingContextErrors(t *testing.T) {
	c := newConnector(&staticTokens{value: "t"})
	if _, err := c.send(context.Background(), replyContext{}, outboundActivity{Type: "message"}); err == nil {
		t.Fatal("expected error for missing serviceURL/conversationID")
	}
}

func TestConnectorSend_SerializesAdaptiveCardAttachment(t *testing.T) {
	var raw map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &raw)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"x"}`))
	}))
	defer srv.Close()

	c := newConnector(&staticTokens{value: "t"})
	rc := replyContext{serviceURL: srv.URL, conversationID: "c"}
	a := newActivity(rc, "message")
	a.Attachments = []attachment{{ContentType: "application/vnd.microsoft.card.adaptive", Content: map[string]any{"type": "AdaptiveCard"}}}

	if _, err := c.send(context.Background(), rc, a); err != nil {
		t.Fatalf("send: %v", err)
	}
	atts, ok := raw["attachments"].([]any)
	if !ok || len(atts) != 1 {
		t.Fatalf("attachments not serialized: %v", raw["attachments"])
	}
	att := atts[0].(map[string]any)
	if att["contentType"] != "application/vnd.microsoft.card.adaptive" {
		t.Errorf("contentType = %v", att["contentType"])
	}
	if content, _ := att["content"].(map[string]any); content["type"] != "AdaptiveCard" {
		t.Errorf("content not round-tripped: %v", att["content"])
	}
}

func TestNewMessageActivity_OmitsAttachmentsWhenNone(t *testing.T) {
	b, _ := json.Marshal(newMessageActivity(replyContext{conversationID: "c"}, "hi"))
	if strings.Contains(string(b), "attachments") {
		t.Errorf("text activity should omit attachments: %s", b)
	}
}

func TestTokenSource_CachesAndReuses(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"abc","token_type":"Bearer","expires_in":3600}`))
	}))
	defer srv.Close()

	ts := newTokenSourceWithURL(config{appID: "id", appPassword: "secret"}, srv.URL)
	for i := 0; i < 3; i++ {
		tok, err := ts.token(context.Background())
		if err != nil {
			t.Fatalf("token: %v", err)
		}
		if tok != "abc" {
			t.Fatalf("token = %q, want abc", tok)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("token endpoint hit %d times, want 1 (cached)", got)
	}
}
