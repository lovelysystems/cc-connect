package core

import (
	"reflect"
	"testing"
)

func writeInChunks(w *commandLineWriter, s string, size int) {
	for len(s) > 0 {
		n := min(size, len(s))
		_, _ = w.Write([]byte(s[:n]))
		s = s[n:]
	}
	w.flush()
}

func TestCommandLineWriter(t *testing.T) {
	const b, e = projectCmdBeginMarker, projectCmdEndMarker
	for _, tc := range []struct {
		name   string
		framed bool
		in     string
		want   []string
	}{
		{"plain lines and last line without ending", false, "a\r\n\nb\nc", []string{"a", "", "b", "c"}},
		{"framed drops noise around the frame", true, "noise\n" + b + "\nout\n\n" + e + "\nlogout\n", []string{"out"}},
		{"framed keeps output without final newline", true, b + "\nout\n" + e + "\n", []string{"out"}},
		{"framed keeps a real trailing empty line", true, b + "\nout\n\n\n" + e + "\n", []string{"out", ""}},
		{"framed keeps empty lines inside", true, b + "\na\n\n\nb\n\n" + e + "\n", []string{"a", "", "", "b"}},
		{"framed cut off before the end marker", true, "x\n" + b + "\na\npart", []string{"a", "part"}},
		{"framed without begin marker gives nothing", true, "noise\nmore", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, size := range []int{1, 3, 1 << 10} {
				var got []string
				writeInChunks(newCommandLineWriter(tc.framed, func(s string) { got = append(got, s) }), tc.in, size)
				if !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("chunk %d: lines = %q, want %q", size, got, tc.want)
				}
			}
		})
	}
}

func TestProjectCommandOutput(t *testing.T) {
	const b, e = projectCmdBeginMarker, projectCmdEndMarker
	for in, want := range map[string]string{
		"noise\n" + b + "\n{\"a\":1}\n\n" + e + "\nlogout\n": "{\"a\":1}\n",
		b + "\nno newline\n" + e + "\n":                     "no newline",
		"x" + b + "\nnot at line start":                     "",
		"noise\n" + b + "\ncut off":                         "cut off",
	} {
		if got := string(projectCommandOutput([]byte(in))); got != want {
			t.Errorf("projectCommandOutput(%q) = %q, want %q", in, got, want)
		}
	}
}
