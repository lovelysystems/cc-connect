package core

import (
	"context"
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
		{path: "/signoff/withdraw", rule: routeProjectScoped, handler: s.handleSignoffWithdraw},
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
		caller := apiCaller{scoped: !all, projects: projects}
		rt.handler(w, r.WithContext(context.WithValue(r.Context(), apiCallerKey{}, caller)))
	})
}

// apiCallerKey keys the caller of an API request in its context.
type apiCallerKey struct{}

// apiCaller is who sent an API request. A caller that is not scoped is
// cc-connect's own user or root and acts for every project; a scoped caller
// acts for its projects alone.
type apiCaller struct {
	scoped   bool
	projects []string
}

func (c apiCaller) allows(project string) bool {
	return !c.scoped || slices.Contains(c.projects, project)
}

// requestCaller returns the caller handle stored on r. A request that did not
// pass through it acts for every project.
func requestCaller(r *http.Request) apiCaller {
	c, _ := r.Context().Value(apiCallerKey{}).(apiCaller)
	return c
}

// cronJobHidden reports whether cron job id exists in a project r's caller
// may not act for. Such a job gets the answer of a missing id.
func (s *APIServer) cronJobHidden(r *http.Request, id string) bool {
	job := s.cron.Store().Get(id)
	return job != nil && !requestCaller(r).allows(job.Project)
}

// timerJobHidden reports whether timer id exists in a project r's caller may
// not act for. Such a timer gets the answer of a missing id.
func (s *APIServer) timerJobHidden(r *http.Request, id string) bool {
	job := s.timer.Store().Get(id)
	return job != nil && !requestCaller(r).allows(job.Project)
}

// callerProject resolves the project a request names for a scoped caller: an
// empty name picks the caller's only project, and a name outside its projects
// is refused with the same answer whether or not that project exists. For a
// caller that is not scoped it returns project unchanged. ok is false once w
// holds the answer.
func callerProject(w http.ResponseWriter, caller apiCaller, project string) (string, bool) {
	if !caller.scoped {
		return project, true
	}
	if project == "" {
		if len(caller.projects) == 1 {
			return caller.projects[0], true
		}
		http.Error(w, "project is required (multiple projects configured)", http.StatusBadRequest)
		return "", false
	}
	if !caller.allows(project) {
		http.Error(w, apiForbidden, http.StatusForbidden)
		return "", false
	}
	return project, true
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
