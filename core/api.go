package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// DefaultMaxAttachmentSize is the default per-attachment size limit (50 MiB)
// applied by the /send API and `cc-connect send` when max_attachment_size_mb
// is unset. Exported so cmd/cc-connect can resolve the same default.
const DefaultMaxAttachmentSize int64 = 50 << 20

// APIServer exposes a local Unix socket API for external tools (e.g. cron jobs)
// to send messages to active sessions.
type APIServer struct {
	socketPath string
	listener   net.Listener
	server     *http.Server
	mux        *http.ServeMux
	engines    map[string]*Engine // project name → engine
	cron       *CronScheduler
	timer      *TimerScheduler
	relay      *RelayManager
	// maxAttachmentBytes caps the raw size of a single attachment accepted by
	// /send; the request body limit in handleSend is derived from it (base64
	// expansion + envelope). Defaults to DefaultMaxAttachmentSize.
	maxAttachmentBytes int64
	mu                 sync.RWMutex

	// signoffCaller reports whether the request comes from a process under
	// the agent process agentPid; nil means callerUnderAgent.
	signoffCaller func(r *http.Request, agentPid int) bool

	// runAsUserIDs maps each run_as_user name to its uid.
	runAsUserIDs map[string]int
	// callerUID returns the uid of the process that sent r; nil means
	// requestPeerUID.
	callerUID func(r *http.Request) (int, error)
}

// SendRequest is the JSON body for POST /send.
//
// Audios and Videos are kept separate from Files so the engine can
// dispatch them to AudioSender / VideoSender (native voice / video
// bubble) instead of FileSender (generic file download). The fields
// reuse FileAttachment as the wire format because audio/video clips
// are byte blobs with a name + mime — the dedicated typing happens at
// the dispatch layer in engine.go. See cc-connect internal task
// t-20260615-cqjbk1.
type SendRequest struct {
	Project    string            `json:"project"`
	SessionKey string            `json:"session_key"`
	Message    string            `json:"message"`
	WorkDir    string            `json:"work_dir,omitempty"`
	CWD        string            `json:"cwd,omitempty"`
	TTSText    string            `json:"tts_text,omitempty"`
	Images     []ImageAttachment `json:"images,omitempty"`
	Files      []FileAttachment  `json:"files,omitempty"`
	Audios     []FileAttachment  `json:"audios,omitempty"`
	Videos     []FileAttachment  `json:"videos,omitempty"`
	AtUsers    []string          `json:"at_users,omitempty"`
	AtAll      bool              `json:"at_all,omitempty"`
}

// NewAPIServer creates an API server on the Unix socket <dataDir>/run/api.sock.
// Without shared the socket is mode 0600, for cc-connect's own user and root.
// With shared it is mode 0666 for the agents of run_as_user projects, opened
// only after its folders passed listenSharedSocket's checks.
func NewAPIServer(dataDir string, shared bool) (*APIServer, error) {
	sockDir := filepath.Join(dataDir, "run")
	if err := os.MkdirAll(sockDir, 0o755); err != nil {
		return nil, fmt.Errorf("create run dir: %w", err)
	}
	var listener net.Listener
	var sockPath string
	if shared {
		var err error
		listener, sockPath, err = listenSharedSocket(sockDir, os.Lstat, os.Stat)
		if err != nil {
			return nil, err
		}
	} else {
		sockPath = filepath.Join(sockDir, "api.sock")

		_ = os.Remove(sockPath)

		var err error
		listener, err = net.Listen("unix", sockPath)
		if err != nil {
			return nil, fmt.Errorf("listen unix socket: %w", err)
		}
		if err := os.Chmod(sockPath, 0o600); err != nil {
			_ = listener.Close()
			return nil, fmt.Errorf("chmod socket: %w", err)
		}
	}

	s := &APIServer{
		socketPath:         sockPath,
		listener:           listener,
		mux:                http.NewServeMux(),
		engines:            make(map[string]*Engine),
		maxAttachmentBytes: DefaultMaxAttachmentSize,
	}
	for _, rt := range s.routes() {
		s.handle(rt)
	}

	return s, nil
}

func (s *APIServer) SocketPath() string {
	return s.socketPath
}

func (s *APIServer) RegisterEngine(name string, e *Engine) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.engines[name] = e
	if s.relay != nil {
		s.relay.RegisterEngine(name, e)
	}
}

func (s *APIServer) SetRelayManager(rm *RelayManager) {
	s.relay = rm
}

func (s *APIServer) RelayManager() *RelayManager {
	return s.relay
}

func (s *APIServer) SetCronScheduler(cs *CronScheduler) {
	s.cron = cs
}

func (s *APIServer) SetTimerScheduler(ts *TimerScheduler) {
	s.timer = ts
}

// SetMaxAttachmentSize overrides the per-attachment size limit (bytes) used by
// /send. Non-positive values are ignored so the default is retained. Safe to
// call at any time, including from the config reload path: it is guarded by
// s.mu because handleSend reads the limit concurrently.
func (s *APIServer) SetMaxAttachmentSize(bytes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if bytes > 0 {
		s.maxAttachmentBytes = bytes
	}
}

// sendBodyEnvelope is the slack added on top of the base64-expanded attachment
// limit when sizing the /send request body: it covers the JSON envelope (field
// names, message text, metadata) and a few sub-limit attachments.
const sendBodyEnvelope int64 = 8 << 20 // 8 MiB

// sendBodyLimit returns the maximum accepted /send request body size in bytes.
// It is derived from the per-attachment limit to accommodate base64 expansion
// (~4/3) plus envelope slack, falling back to DefaultMaxAttachmentSize when no
// limit has been set (e.g. APIServer zero value in tests). Callers in hot paths
// (handleSend) run concurrently with SetMaxAttachmentSize, so the read is
// guarded by s.mu.
func (s *APIServer) sendBodyLimit() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	limit := s.maxAttachmentBytes
	if limit <= 0 {
		limit = DefaultMaxAttachmentSize
	}
	return limit*4/3 + sendBodyEnvelope
}

func (s *APIServer) Start() {
	s.server = &http.Server{
		Handler: s.mux,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			return context.WithValue(ctx, apiConnKey{}, c)
		},
	}
	go func() {
		if err := s.server.Serve(s.listener); err != nil && err != http.ErrServerClosed {
			slog.Error("api server error", "error", err)
		}
	}()
	slog.Info("api server started", "socket", s.socketPath)
}

func (s *APIServer) Stop() {
	if s.server != nil {
		if err := s.server.Close(); err != nil && err != http.ErrServerClosed {
			slog.Debug("api server close failed", "error", err)
		}
	}
	if err := os.Remove(s.socketPath); err != nil && !os.IsNotExist(err) {
		slog.Debug("api server remove socket failed", "error", err)
	}
}

func apiJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("api server: write JSON failed", "error", err)
	}
}

func (s *APIServer) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	// Attachments travel base64-encoded inside the JSON body (~4/3 expansion)
	// plus the request envelope, so size the reader to fit one max-size
	// attachment with overhead to spare. The previous hard-coded 52 MB cap was
	// smaller than a single 50 MB attachment after base64 encoding and would
	// reject valid sends; deriving it from maxAttachmentBytes keeps the body
	// limit in step with the configured attachment limit.
	var req SendRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, s.sendBodyLimit())).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Message == "" && strings.TrimSpace(req.TTSText) == "" && len(req.Images) == 0 && len(req.Files) == 0 && len(req.Audios) == 0 && len(req.Videos) == 0 {
		http.Error(w, "message, tts_text, or attachment is required", http.StatusBadRequest)
		return
	}
	caller := requestCaller(r)
	project, allowed := callerProject(w, caller, req.Project)
	if !allowed {
		return
	}
	// cc-connect would stat a work_dir as its own user, so a scoped caller
	// could learn which paths exist outside its reach.
	if caller.scoped && (req.WorkDir != "" || req.CWD != "") {
		http.Error(w, apiForbidden, http.StatusForbidden)
		return
	}
	req.Project = project

	s.mu.RLock()
	var engine *Engine
	var ok bool
	if req.Project != "" {
		engine, ok = s.engines[req.Project]
	} else if len(s.engines) == 1 {
		// No project specified and only one engine: use it by default.
		// Do NOT silently fall back when a non-empty project name is unknown —
		// that misroutes the message to the wrong engine. Mirrors the resolve
		// pattern in webhook.go and handleCronAdd.
		for _, e := range s.engines {
			engine = e
			ok = true
		}
	}
	s.mu.RUnlock()

	if !ok {
		if req.Project == "" {
			http.Error(w, "project is required (multiple projects configured)", http.StatusBadRequest)
			return
		}
		http.Error(w, fmt.Sprintf("project %q not found", req.Project), http.StatusNotFound)
		return
	}

	workDir := req.WorkDir
	if workDir == "" {
		workDir = req.CWD
	}
	if req.Message != "" || len(req.Images) > 0 || len(req.Files) > 0 {
		if err := engine.SendToSessionWithOptions(req.SessionKey, req.Message, req.Images, req.Files, SendOptions{WorkDir: workDir, AtUsers: req.AtUsers, AtAll: req.AtAll}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if len(req.Audios) > 0 {
		if err := engine.SendAudiosToSession(req.SessionKey, req.Audios); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if len(req.Videos) > 0 {
		if err := engine.SendVideosToSession(req.SessionKey, req.Videos); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if strings.TrimSpace(req.TTSText) != "" {
		if err := engine.SendTTSToSession(req.SessionKey, req.TTSText); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	apiJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *APIServer) handleSessions(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	type sessionInfo struct {
		Project    string `json:"project"`
		SessionKey string `json:"session_key"`
		Platform   string `json:"platform"`
	}

	var result []sessionInfo
	for name, e := range s.engines {
		e.interactiveMu.Lock()
		for key, state := range e.interactiveStates {
			if state.platform != nil {
				result = append(result, sessionInfo{
					Project:    name,
					SessionKey: key,
					Platform:   state.platform.Name(),
				})
			}
		}
		e.interactiveMu.Unlock()
	}

	apiJSON(w, http.StatusOK, result)
}

// ── Cron API ───────────────────────────────────────────────────

// CronAddRequest is the JSON body for POST /cron/add.
type CronAddRequest struct {
	Project     string `json:"project"`
	SessionKey  string `json:"session_key"`
	CronExpr    string `json:"cron_expr"`
	Prompt      string `json:"prompt"`
	Exec        string `json:"exec"`
	WorkDir     string `json:"work_dir"`
	Description string `json:"description"`
	Silent      *bool  `json:"silent,omitempty"`
	SessionMode string `json:"session_mode,omitempty"`
	Mode        string `json:"mode,omitempty"`
	TimeoutMins *int   `json:"timeout_mins,omitempty"`
}

func (s *APIServer) handleCronAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.cron == nil {
		http.Error(w, "cron scheduler not available", http.StatusServiceUnavailable)
		return
	}

	var req CronAddRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.CronExpr == "" {
		http.Error(w, "cron_expr is required", http.StatusBadRequest)
		return
	}
	if req.Prompt == "" && req.Exec == "" {
		http.Error(w, "either prompt or exec is required", http.StatusBadRequest)
		return
	}
	if req.Prompt != "" && req.Exec != "" {
		http.Error(w, "prompt and exec are mutually exclusive", http.StatusBadRequest)
		return
	}

	// Resolve project: use provided, or pick single engine
	project, allowed := callerProject(w, requestCaller(r), req.Project)
	if !allowed {
		return
	}
	if project == "" {
		s.mu.RLock()
		if len(s.engines) == 1 {
			for name := range s.engines {
				project = name
			}
		}
		s.mu.RUnlock()
	}
	if project == "" {
		http.Error(w, "project is required (multiple projects configured)", http.StatusBadRequest)
		return
	}

	// Resolve session_key: use provided, or auto-detect from active sessions
	sessionKey := req.SessionKey
	if sessionKey == "" {
		s.mu.RLock()
		engine := s.engines[project]
		s.mu.RUnlock()
		if engine != nil {
			keys := engine.ActiveSessionKeys()
			if len(keys) == 1 {
				sessionKey = keys[0]
				slog.Debug("auto-detected session_key for cron job", "session_key", sessionKey)
			}
		}
	}
	if sessionKey == "" {
		http.Error(w, "session_key is required: set CC_SESSION_KEY env, pass --session-key, or ensure exactly one active session exists", http.StatusBadRequest)
		return
	}

	job := &CronJob{
		ID:          GenerateCronID(),
		Project:     project,
		SessionKey:  sessionKey,
		CronExpr:    req.CronExpr,
		Prompt:      req.Prompt,
		Exec:        req.Exec,
		WorkDir:     req.WorkDir,
		Description: req.Description,
		Enabled:     true,
		Silent:      req.Silent,
		SessionMode: NormalizeCronSessionMode(req.SessionMode),
		Mode:        req.Mode,
		TimeoutMins: req.TimeoutMins,
	}
	job.CreatedAt = time.Now()

	if err := s.cron.AddJob(job); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	apiJSON(w, http.StatusOK, job)
}

func (s *APIServer) handleCronList(w http.ResponseWriter, r *http.Request) {
	if s.cron == nil {
		http.Error(w, "cron scheduler not available", http.StatusServiceUnavailable)
		return
	}

	caller := requestCaller(r)
	project := r.URL.Query().Get("project")
	if project != "" && !caller.allows(project) {
		http.Error(w, apiForbidden, http.StatusForbidden)
		return
	}
	var jobs []*CronJob
	if project != "" {
		jobs = s.cron.Store().ListByProject(project)
	} else {
		jobs = s.cron.Store().List()
	}
	if caller.scoped {
		jobs = slices.DeleteFunc(jobs, func(j *CronJob) bool { return !caller.allows(j.Project) })
	}

	apiJSON(w, http.StatusOK, jobs)
}

func (s *APIServer) handleCronDel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.cron == nil {
		http.Error(w, "cron scheduler not available", http.StatusServiceUnavailable)
		return
	}

	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.ID == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}

	if !s.cronJobHidden(r, req.ID) && s.cron.RemoveJob(req.ID) {
		apiJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	} else {
		http.Error(w, fmt.Sprintf("job %q not found", req.ID), http.StatusNotFound)
	}
}

func (s *APIServer) handleCronExec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.cron == nil {
		http.Error(w, "cron scheduler not available", http.StatusServiceUnavailable)
		return
	}

	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.ID == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}

	var err error
	if s.cronJobHidden(r, req.ID) {
		err = fmt.Errorf("%w: %q", ErrCronJobNotFound, req.ID)
	} else {
		err = s.cron.RunJobNow(req.ID)
	}
	if err != nil {
		if errors.Is(err, ErrCronJobNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	apiJSON(w, http.StatusAccepted, map[string]string{
		"id":     req.ID,
		"status": "triggered",
	})
}

func (s *APIServer) handleCronInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if s.cron == nil {
		http.Error(w, "cron scheduler not available", http.StatusServiceUnavailable)
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}

	job := s.cron.store.Get(id)
	if job == nil || !requestCaller(r).allows(job.Project) {
		http.Error(w, fmt.Sprintf("job %q not found", id), http.StatusNotFound)
		return
	}

	apiJSON(w, http.StatusOK, job)
}

func (s *APIServer) handleCronEdit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.cron == nil {
		http.Error(w, "cron scheduler not available", http.StatusServiceUnavailable)
		return
	}

	var req struct {
		ID    string `json:"id"`
		Field string `json:"field"`
		Value any    `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.ID == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	if req.Field == "" {
		http.Error(w, "field is required", http.StatusBadRequest)
		return
	}
	if req.Value == nil {
		http.Error(w, "value is required", http.StatusBadRequest)
		return
	}

	if s.cronJobHidden(r, req.ID) {
		http.Error(w, fmt.Sprintf("job %q not found", req.ID), http.StatusBadRequest)
		return
	}
	if project, ok := req.Value.(string); ok && req.Field == "project" && !requestCaller(r).allows(project) {
		http.Error(w, apiForbidden, http.StatusForbidden)
		return
	}
	if err := s.cron.UpdateJob(req.ID, req.Field, req.Value); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Return updated job
	job := s.cron.Store().Get(req.ID)
	apiJSON(w, http.StatusOK, job)
}

// ── Timer API ─────────────────────────────────────────────────

// TimerAddRequest is the JSON body for POST /timer/add.
type TimerAddRequest struct {
	Project     string `json:"project"`
	SessionKey  string `json:"session_key"`
	Delay       string `json:"delay"` // relative ("2h") or absolute ISO time
	Prompt      string `json:"prompt"`
	Exec        string `json:"exec"`
	WorkDir     string `json:"work_dir"`
	Description string `json:"description"`
	Silent      *bool  `json:"silent,omitempty"`
	Mute        bool   `json:"mute,omitempty"`
	SessionMode string `json:"session_mode,omitempty"`
	Mode        string `json:"mode,omitempty"`
	TimeoutMins *int   `json:"timeout_mins,omitempty"`
}

func (s *APIServer) handleTimerAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.timer == nil {
		http.Error(w, "timer scheduler not available", http.StatusServiceUnavailable)
		return
	}

	var req TimerAddRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Delay == "" {
		http.Error(w, "delay is required (e.g. 2h, 30m, or ISO time)", http.StatusBadRequest)
		return
	}
	if req.Prompt == "" && req.Exec == "" {
		http.Error(w, "either prompt or exec is required", http.StatusBadRequest)
		return
	}
	if req.Prompt != "" && req.Exec != "" {
		http.Error(w, "prompt and exec are mutually exclusive", http.StatusBadRequest)
		return
	}

	fireAt, err := ParseDelayOrTime(req.Delay)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	project, allowed := callerProject(w, requestCaller(r), req.Project)
	if !allowed {
		return
	}
	if project == "" {
		s.mu.RLock()
		if len(s.engines) == 1 {
			for name := range s.engines {
				project = name
			}
		}
		s.mu.RUnlock()
	}
	if project == "" {
		http.Error(w, "project is required (multiple projects configured)", http.StatusBadRequest)
		return
	}

	sessionKey := req.SessionKey
	if sessionKey == "" {
		s.mu.RLock()
		engine := s.engines[project]
		s.mu.RUnlock()
		if engine != nil {
			keys := engine.ActiveSessionKeys()
			if len(keys) == 1 {
				sessionKey = keys[0]
			}
		}
	}
	if sessionKey == "" {
		http.Error(w, "session_key is required", http.StatusBadRequest)
		return
	}

	job := &TimerJob{
		ID:          GenerateTimerID(),
		Project:     project,
		SessionKey:  sessionKey,
		ScheduledAt: fireAt,
		Prompt:      req.Prompt,
		Exec:        req.Exec,
		WorkDir:     req.WorkDir,
		Description: req.Description,
		Silent:      req.Silent,
		Mute:        req.Mute,
		SessionMode: req.SessionMode,
		Mode:        req.Mode,
		TimeoutMins: req.TimeoutMins,
		CreatedAt:   time.Now(),
	}

	if err := s.timer.AddJob(job); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	apiJSON(w, http.StatusOK, job)
}

func (s *APIServer) handleTimerList(w http.ResponseWriter, r *http.Request) {
	if s.timer == nil {
		http.Error(w, "timer scheduler not available", http.StatusServiceUnavailable)
		return
	}

	caller := requestCaller(r)
	project := r.URL.Query().Get("project")
	if project != "" && !caller.allows(project) {
		http.Error(w, apiForbidden, http.StatusForbidden)
		return
	}
	var jobs []*TimerJob
	if project != "" {
		jobs = s.timer.Store().ListByProject(project)
	} else {
		jobs = s.timer.Store().List()
	}

	// Filter to pending only
	var pending []*TimerJob
	for _, j := range jobs {
		if !j.Fired && caller.allows(j.Project) {
			pending = append(pending, j)
		}
	}

	apiJSON(w, http.StatusOK, pending)
}

func (s *APIServer) handleTimerInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if s.timer == nil {
		http.Error(w, "timer scheduler not available", http.StatusServiceUnavailable)
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}

	job := s.timer.Store().Get(id)
	if job == nil || !requestCaller(r).allows(job.Project) {
		http.Error(w, "timer not found", http.StatusNotFound)
		return
	}

	apiJSON(w, http.StatusOK, job)
}

func (s *APIServer) handleTimerDel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.timer == nil {
		http.Error(w, "timer scheduler not available", http.StatusServiceUnavailable)
		return
	}

	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.ID == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}

	if s.timerJobHidden(r, req.ID) || !s.timer.RemoveJob(req.ID) {
		http.Error(w, "timer not found", http.StatusNotFound)
		return
	}

	apiJSON(w, http.StatusOK, map[string]string{"status": "ok", "id": req.ID})
}

// ── Signoff API ──────────────────────────────────────────────

// signoffBodyLimit caps the /signoff/send request body.
const signoffBodyLimit = 1 << 20

// handleSignoffSend accepts a signoff card for the thread of session_key. It
// answers posted before the platform has the card; a post that fails later
// shows in the log only.
func (s *APIServer) handleSignoffSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req SignoffRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, signoffBodyLimit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := req.validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	engine, target, ok := s.signoffEngine(w, r, req.SessionKey, req.RequestID)
	if !ok {
		return
	}
	if engine.runAsUser() == "" {
		http.Error(w, "project has no run_as_user", http.StatusUnprocessableEntity)
		return
	}
	if _, ok := target.platform.(SignoffCardPlatform); !ok {
		http.Error(w, fmt.Sprintf("platform %q cannot show signoff cards", target.platform.Name()), http.StatusUnprocessableEntity)
		return
	}
	if !engine.acceptSignoff(target, req) {
		http.Error(w, "request_id and nonce were already used", http.StatusConflict)
		return
	}
	apiJSON(w, http.StatusOK, map[string]string{"status": "posted"})
}

// handleSignoffWithdraw takes back a card of session_key that waits for a
// click. The card is drawn as a replaced one and the agent gets no report.
func (s *APIServer) handleSignoffWithdraw(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		SessionKey string `json:"session_key"`
		RequestID  string `json:"request_id"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, signoffBodyLimit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.SessionKey == "" || req.RequestID == "" {
		http.Error(w, "session_key and request_id are required", http.StatusBadRequest)
		return
	}
	engine, _, ok := s.signoffEngine(w, r, req.SessionKey, req.RequestID)
	if !ok {
		return
	}
	if !engine.withdrawSignoff(req.SessionKey, req.RequestID) {
		http.Error(w, "no open signoff card for request_id", http.StatusNotFound)
		return
	}
	apiJSON(w, http.StatusOK, map[string]string{"status": "withdrawn"})
}

// signoffEngine finds the engine running the live agent of sessionKey that
// r's caller runs under, among the projects the caller may act for. ok is
// false once w holds the answer: 404 with no such live session, 403 when the
// caller is under none of their agents.
func (s *APIServer) signoffEngine(w http.ResponseWriter, r *http.Request, sessionKey, requestID string) (engine *Engine, target signoffTarget, ok bool) {
	s.mu.RLock()
	names := slices.Sorted(maps.Keys(s.engines))
	engines := make([]*Engine, 0, len(names))
	for _, name := range names {
		engines = append(engines, s.engines[name])
	}
	s.mu.RUnlock()

	callerUnder := s.signoffCaller
	if callerUnder == nil {
		callerUnder = callerUnderAgent
	}
	caller := requestCaller(r)
	live := false
	for i, e := range engines {
		if !caller.allows(names[i]) {
			continue
		}
		t, ok := e.signoffTarget(sessionKey)
		if !ok {
			continue
		}
		live = true
		if callerUnder(r, t.agentPid) {
			return e, t, true
		}
	}
	if !live {
		http.Error(w, "no live session for session_key", http.StatusNotFound)
		return nil, target, false
	}
	slog.Warn("signoff: caller is not under the session's agent", "session_key", sessionKey, "request_id", requestID)
	http.Error(w, "caller is not under the agent of session_key", http.StatusForbidden)
	return nil, target, false
}

// ── Relay API ──────────────────────────────────────────────────

func (s *APIServer) handleRelaySend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.relay == nil {
		http.Error(w, "relay not available", http.StatusServiceUnavailable)
		return
	}

	var req RelayRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.To == "" || req.Message == "" || req.SessionKey == "" {
		http.Error(w, "to, session_key, and message are required", http.StatusBadRequest)
		return
	}
	// A scoped caller reaches only agents its own project shares a chat with.
	if caller := requestCaller(r); caller.scoped && (req.From == "" || !caller.allows(req.From) || !s.relay.boundInChat(req.SessionKey, req.From)) {
		http.Error(w, apiForbidden, http.StatusForbidden)
		return
	}

	resp, err := s.relay.Send(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	apiJSON(w, http.StatusOK, resp)
}

func (s *APIServer) handleRelayBind(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.relay == nil {
		http.Error(w, "relay not available", http.StatusServiceUnavailable)
		return
	}

	var req struct {
		Platform string            `json:"platform"`
		ChatID   string            `json:"chat_id"`
		Bots     map[string]string `json:"bots"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.ChatID == "" || len(req.Bots) < 2 {
		http.Error(w, "chat_id and at least 2 bots are required", http.StatusBadRequest)
		return
	}

	s.relay.Bind(req.Platform, req.ChatID, req.Bots)
	apiJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *APIServer) handleRelayBinding(w http.ResponseWriter, r *http.Request) {
	if s.relay == nil {
		http.Error(w, "relay not available", http.StatusServiceUnavailable)
		return
	}
	chatID := r.URL.Query().Get("chat_id")
	if chatID == "" {
		http.Error(w, "chat_id is required", http.StatusBadRequest)
		return
	}
	binding := s.relay.GetBinding(chatID)
	if binding == nil {
		http.Error(w, "no binding found", http.StatusNotFound)
		return
	}
	apiJSON(w, http.StatusOK, binding)
}
