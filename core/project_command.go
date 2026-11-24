package core

import (
	"bytes"
	"strings"
)

// projectCommand is a shell command cc-connect runs for a project: the
// project's shell, flag and profile, the command text, the folder it runs in
// and the variables it gets on top of what runs it.
type projectCommand struct {
	shell   string
	flag    string
	profile string
	command string
	dir     string
	// env holds KEY=VALUE pairs the command gets under run_as_user. Without
	// run_as_user the command keeps cc-connect's own environment and the
	// caller decides what to add.
	env []string
}

// Under run_as_user a project command runs through sudo -i, whose login shell
// may print before and after it. The command runs inside a script that prints
// these marker lines around it on stdout and on stderr; readers keep only
// what lies between them.
const (
	projectCmdBeginMarker = "@@cc-connect-cmd-begin"
	projectCmdEndMarker   = "@@cc-connect-cmd-end"
)

// projectCommandOutput returns what a framed command printed on one stream
// between the begin marker line and the end marker. Output cut off before the
// end marker is kept up to where it stops.
func projectCommandOutput(out []byte) []byte {
	begin := []byte(projectCmdBeginMarker + "\n")
	start := 0
	if !bytes.HasPrefix(out, begin) {
		i := bytes.Index(out, append([]byte("\n"), begin...))
		if i < 0 {
			return nil
		}
		start = i + 1
	}
	rest := out[start+len(begin):]
	if stop := bytes.Index(rest, []byte("\n"+projectCmdEndMarker+"\n")); stop >= 0 {
		return rest[:stop]
	}
	return rest
}

// commandLineWriter splits what a command writes on one stream into lines and
// hands each line to emit, without its line ending. When framed, it hands on
// only the lines between the begin and end marker lines. Call flush once the
// command has ended to hand on a last line that has no line ending.
type commandLineWriter struct {
	emit    func(string)
	framed  bool
	inside  bool
	done    bool
	partial []byte
	// heldEmpty is an empty line kept back because it may be the separator
	// the script prints before the end marker.
	heldEmpty bool
}

func newCommandLineWriter(framed bool, emit func(string)) *commandLineWriter {
	return &commandLineWriter{emit: emit, framed: framed, inside: !framed}
}

func (w *commandLineWriter) Write(p []byte) (int, error) {
	data := p
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			w.partial = append(w.partial, data...)
			break
		}
		line := data[:i]
		if len(w.partial) > 0 {
			line = append(w.partial, line...)
			w.partial = nil
		}
		w.line(strings.TrimSuffix(string(line), "\r"))
		data = data[i+1:]
	}
	return len(p), nil
}

func (w *commandLineWriter) line(s string) {
	switch {
	case w.done:
	case !w.framed:
		w.emit(s)
	case !w.inside:
		w.inside = s == projectCmdBeginMarker
	case s == projectCmdEndMarker:
		w.done = true
		w.heldEmpty = false
	case s == "":
		if w.heldEmpty {
			w.emit("")
		}
		w.heldEmpty = true
	default:
		if w.heldEmpty {
			w.emit("")
			w.heldEmpty = false
		}
		w.emit(s)
	}
}

func (w *commandLineWriter) flush() {
	if len(w.partial) == 0 {
		return
	}
	s := strings.TrimSuffix(string(w.partial), "\r")
	w.partial = nil
	if w.done || !w.inside {
		return
	}
	if w.heldEmpty {
		w.emit("")
		w.heldEmpty = false
	}
	w.emit(s)
}
