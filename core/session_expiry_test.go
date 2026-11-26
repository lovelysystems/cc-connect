package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// workDirResultAgent is a resultAgent whose work dir is a test directory, so
// the attachment sweep never touches the package directory.
type workDirResultAgent struct {
	resultAgent
	dir string
}

func (a *workDirResultAgent) GetWorkDir() string { return a.dir }

func ageSession(s *Session, age time.Duration) {
	s.mu.Lock()
	s.CreatedAt = time.Now().Add(-age)
	s.mu.Unlock()
}

func newExpiryEngine(t *testing.T, reply string) (*Engine, *stubPlatformEngine, string) {
	t.Helper()
	dir := t.TempDir()
	p := &stubPlatformEngine{n: "test"}
	agent := &workDirResultAgent{resultAgent: resultAgent{session: newResultAgentSession(reply)}, dir: dir}
	e := NewEngine("test", agent, []Platform{p}, filepath.Join(dir, "sessions.json"), LangEnglish)
	e.SetSessionMaxAge(90 * 24 * time.Hour)
	return e, p, dir
}

func TestSessionsCreatedBefore_ReturnsOnlyOldSessionsWithTheirKey(t *testing.T) {
	sm := NewSessionManager("")
	old := sm.GetOrCreateActive("test:old")
	ageSession(old, 91*24*time.Hour)
	sm.GetOrCreateActive("test:young")

	got := sm.SessionsCreatedBefore(time.Now().Add(-90 * 24 * time.Hour))
	if len(got) != 1 || got[0].Session.ID != old.ID || got[0].Key != "test:old" {
		t.Fatalf("SessionsCreatedBefore = %+v, want only %s under test:old", got, old.ID)
	}
}

func TestExpireSessions_RemovesHistoryNamesAndUserMeta(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	sm := NewSessionManager(path)
	s := sm.GetOrCreateActive("test:thread")
	s.AddHistory("user", "hello")
	s.AddHistory("assistant", "hi")
	s.SetAgentSessionID("agent-1", "stub")
	sm.SetSessionName("agent-1", "Thread about pages")
	sm.UpdateUserMeta("test:thread", "Editor Name", "Newsroom")

	sessions, messages := sm.ExpireSessions([]string{s.ID})
	if sessions != 1 || messages != 2 {
		t.Fatalf("ExpireSessions = (%d, %d), want (1, 2)", sessions, messages)
	}

	reloaded := NewSessionManager(path)
	if reloaded.FindByID(s.ID) != nil {
		t.Fatal("expired session still in the saved file")
	}
	if reloaded.GetUserMeta("test:thread") != nil {
		t.Fatal("user meta of an expired key still in the saved file")
	}
	if reloaded.GetSessionName("agent-1") != "" {
		t.Fatal("custom session name still in the saved file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"hello", "Editor Name", "Newsroom", "Thread about pages"} {
		if strings.Contains(string(data), leaked) {
			t.Fatalf("saved file still contains %q", leaked)
		}
	}
}

func TestExpireSessions_KeepsUserMetaWhileTheKeyHasAnotherSession(t *testing.T) {
	sm := NewSessionManager("")
	old := sm.GetOrCreateActive("test:thread")
	sm.NewSession("test:thread", "")
	sm.UpdateUserMeta("test:thread", "Editor Name", "")

	sm.ExpireSessions([]string{old.ID})

	if sm.GetUserMeta("test:thread") == nil {
		t.Fatal("user meta dropped although the key still has a session")
	}
}

func TestSweepExpiredSessions_DeletesOldSessionAndKeepsYoungOne(t *testing.T) {
	e, _, _ := newExpiryEngine(t, "unused")
	old := e.sessions.GetOrCreateActive("test:old")
	old.AddHistory("user", "old question")
	ageSession(old, 91*24*time.Hour)
	young := e.sessions.GetOrCreateActive("test:young")
	young.AddHistory("user", "recent question")
	ageSession(young, 30*24*time.Hour)

	e.sweepExpiredSessions()

	if e.sessions.FindByID(old.ID) != nil {
		t.Fatal("session older than the max age survived the sweep")
	}
	if e.sessions.FindByID(young.ID) == nil || len(young.GetHistory(0)) != 1 {
		t.Fatal("young session or its history was deleted")
	}
}

func TestSweepExpiredSessions_SkipsBusySessionUntilItIsFree(t *testing.T) {
	e, _, _ := newExpiryEngine(t, "unused")
	s := e.sessions.GetOrCreateActive("test:busy")
	ageSession(s, 91*24*time.Hour)
	if !s.TryLock() {
		t.Fatal("could not lock session")
	}

	e.sweepExpiredSessions()
	if e.sessions.FindByID(s.ID) == nil {
		t.Fatal("busy session was deleted mid-turn")
	}

	s.Unlock()
	e.sweepExpiredSessions()
	if e.sessions.FindByID(s.ID) != nil {
		t.Fatal("session was not deleted once it was free")
	}
}

func TestSweepExpiredSessions_ClosesLiveAgentOfExpiredSession(t *testing.T) {
	e, _, _ := newExpiryEngine(t, "unused")
	key := "test:live"
	s := e.sessions.GetOrCreateActive(key)
	ageSession(s, 91*24*time.Hour)
	live := newControllableSession("live-agent")
	e.interactiveStates[key] = &interactiveState{agentSession: live}

	e.sweepExpiredSessions()

	if live.Alive() {
		t.Fatal("live agent process of the expired session is still running")
	}
	if _, ok := e.interactiveStates[key]; ok {
		t.Fatal("interactive state of the expired session is still registered")
	}
}

func TestSweepExpiredSessions_DisabledDeletesNothing(t *testing.T) {
	e, _, _ := newExpiryEngine(t, "unused")
	e.SetSessionMaxAge(0)
	s := e.sessions.GetOrCreateActive("test:old")
	ageSession(s, 400*24*time.Hour)

	e.sweepExpiredSessions()

	if e.sessions.FindByID(s.ID) == nil {
		t.Fatal("session deleted although expiry is disabled")
	}
}

func TestSweepExpiredSessions_NextMessageStartsFreshSession(t *testing.T) {
	e, p, _ := newExpiryEngine(t, "fresh reply")
	key := "test:thread"
	old := e.sessions.GetOrCreateActive(key)
	old.AddHistory("user", "old question")
	old.SetAgentSessionID("old-agent", "stub")
	ageSession(old, 91*24*time.Hour)
	e.sweepExpiredSessions()

	e.handleMessage(p, &Message{SessionKey: key, Platform: "test", UserID: "u1", Content: "new question", ReplyCtx: "ctx"})

	active := waitForHistory(t, e, key, 2)
	if active.ID == old.ID {
		t.Fatal("message continued the expired session")
	}
	if got := active.GetHistory(0)[0].Content; got != "new question" {
		t.Fatalf("first history entry = %q, want the new question", got)
	}
}

func TestHandleMessage_ReplacesExpiredSessionBeforeTheTurn(t *testing.T) {
	e, p, _ := newExpiryEngine(t, "fresh reply")
	key := "test:thread"
	old := e.sessions.GetOrCreateActive(key)
	old.AddHistory("user", "old question")
	old.SetAgentSessionID("old-agent", "stub")
	ageSession(old, 91*24*time.Hour)

	e.handleMessage(p, &Message{SessionKey: key, Platform: "test", UserID: "u1", Content: "new question", ReplyCtx: "ctx"})

	active := waitForHistory(t, e, key, 2)
	if active.ID == old.ID {
		t.Fatal("turn ran on the expired session")
	}
	if active.GetAgentSessionID() == "old-agent" {
		t.Fatal("new session resumes the expired agent session")
	}
	if e.sessions.FindByID(old.ID) != nil {
		t.Fatal("expired session was kept after it was replaced")
	}
	for _, line := range p.getSent() {
		if strings.Contains(line, "error") {
			t.Fatalf("editor got an error reply: %v", p.getSent())
		}
	}
}

func TestRemoveAttachmentsOlderThan_DeletesOldFilesAndEmptiedDirs(t *testing.T) {
	workDir := t.TempDir()
	root := attachmentsDir(workDir)
	oldDir := filepath.Join(root, "msg-old")
	mixedDir := filepath.Join(root, "msg-mixed")
	for _, d := range []string{oldDir, mixedDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	oldTime := time.Now().Add(-91 * 24 * time.Hour)
	write := func(path string, mtime time.Time) {
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(oldDir, "a.pdf"), oldTime)
	write(filepath.Join(mixedDir, "b.pdf"), oldTime)
	write(filepath.Join(mixedDir, "c.pdf"), time.Now().Add(-24*time.Hour))

	removed, err := RemoveAttachmentsOlderThan(workDir, time.Now().Add(-90*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatal("emptied per-message directory was kept")
	}
	if _, err := os.Stat(filepath.Join(mixedDir, "c.pdf")); err != nil {
		t.Fatal("young attachment was deleted")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal("attachments root was removed")
	}
}

func TestRemoveAttachmentsOlderThan_NoAttachmentsDirIsNotAnError(t *testing.T) {
	removed, err := RemoveAttachmentsOlderThan(t.TempDir(), time.Now())
	if err != nil || removed != 0 {
		t.Fatalf("RemoveAttachmentsOlderThan = (%d, %v), want (0, nil)", removed, err)
	}
}

func waitForHistory(t *testing.T, e *Engine, key string, n int) *Session {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		active := e.sessions.GetOrCreateActive(key)
		if len(active.GetHistory(0)) >= n {
			return active
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %d history entries on %s", n, key)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}
