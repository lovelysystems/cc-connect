package core

import (
	"log/slog"
	"maps"
	"net/http"
	"os"
	"slices"
)

// apiForbidden is the answer to a caller the API refuses.
const apiForbidden = "forbidden"

// routeRule says who may call an API route. The zero rule admits
// cc-connect's own user and root only.
type routeRule int

const (
	routeOwnUser routeRule = iota
	// routeProjectScoped also admits the user a project's agent runs as; the
	// handler checks each project the request touches.
	routeProjectScoped
)

type apiRoute struct {
	path    string
	rule    routeRule
	handler http.HandlerFunc
}

// routes lists the API routes and who may call each.
func (s *APIServer) routes() []apiRoute {
	return []apiRoute{
		{path: "/send", rule: routeProjectScoped, handler: s.handleSend},
		{path: "/sessions", rule: routeOwnUser, handler: s.handleSessions},
		{path: "/cron/add", rule: routeProjectScoped, handler: s.handleCronAdd},
		{path: "/cron/list", rule: routeProjectScoped, handler: s.handleCronList},
		{path: "/cron/info", rule: routeProjectScoped, handler: s.handleCronInfo},
		{path: "/cron/edit", rule: routeProjectScoped, handler: s.handleCronEdit},
		{path: "/cron/del", rule: routeProjectScoped, handler: s.handleCronDel},
		{path: "/cron/exec", rule: routeProjectScoped, handler: s.handleCronExec},
		{path: "/cron/run", rule: routeProjectScoped, handler: s.handleCronExec},
		{path: "/timer/add", rule: routeProjectScoped, handler: s.handleTimerAdd},
		{path: "/timer/list", rule: routeProjectScoped, handler: s.handleTimerList},
		{path: "/timer/info", rule: routeProjectScoped, handler: s.handleTimerInfo},
		{path: "/timer/del", rule: routeProjectScoped, handler: s.handleTimerDel},
		{path: "/relay/send", rule: routeProjectScoped, handler: s.handleRelaySend},
		{path: "/relay/bind", rule: routeOwnUser, handler: s.handleRelayBind},
		{path: "/relay/binding", rule: routeOwnUser, handler: s.handleRelayBinding},
		{path: "/signoff/send", rule: routeProjectScoped, handler: s.handleSignoffSend},
	}
}

// handle registers rt on the server's mux behind its rule. A refused caller
// gets 403 before the handler reads the request body.
func (s *APIServer) handle(rt apiRoute) {
	s.mux.HandleFunc(rt.path, func(w http.ResponseWriter, r *http.Request) {
		uid, err := s.requestUID(r)
		if err != nil {
			slog.Warn("api: caller uid unavailable", "path", rt.path, "error", err)
			http.Error(w, apiForbidden, http.StatusForbidden)
			return
		}
		all, projects := s.CallerProjects(uid)
		if !all && (rt.rule == routeOwnUser || len(projects) == 0) {
			slog.Debug("api: caller refused", "path", rt.path, "uid", uid)
			http.Error(w, apiForbidden, http.StatusForbidden)
			return
		}
		rt.handler(w, r)
	})
}

// SetRunAsUserIDs sets the uid of each run_as_user name.
func (s *APIServer) SetRunAsUserIDs(ids map[string]int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runAsUserIDs = maps.Clone(ids)
}

// CallerProjects reports whether uid may act for every project and, when it
// may not, the projects whose run_as_user has that uid, sorted.
func (s *APIServer) CallerProjects(uid int) (all bool, projects []string) {
	if uid == 0 || uid == os.Geteuid() {
		return true, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for name, e := range s.engines {
		user := e.projectRunAs.RunAsUser
		if id, ok := s.runAsUserIDs[user]; ok && user != "" && id == uid {
			projects = append(projects, name)
		}
	}
	slices.Sort(projects)
	return false, projects
}

func (s *APIServer) requestUID(r *http.Request) (int, error) {
	if s.callerUID != nil {
		return s.callerUID(r)
	}
	return requestPeerUID(r, SocketPeerCredentialsSupported())
}
