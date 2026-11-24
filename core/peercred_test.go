package core

import (
	"errors"
	"testing"
)

func TestPeerCredDescendsFrom(t *testing.T) {
	parents := map[int]int{300: 200, 200: 100, 100: 1, 400: 200, 500: 1}
	parentOf := func(pid int) (int, error) {
		if p, ok := parents[pid]; ok {
			return p, nil
		}
		return 0, errors.New("no such process")
	}
	cases := []struct {
		name          string
		pid, ancestor int
		want          bool
	}{
		{"grandchild of the agent", 300, 100, true},
		{"the agent itself", 100, 100, true},
		{"a sibling branch", 400, 300, false},
		{"under init only", 500, 100, false},
		{"lookup error", 999, 100, false},
		{"agent pid 0", 300, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := descendsFrom(c.pid, c.ancestor, parentOf); got != c.want {
				t.Fatalf("descendsFrom(%d, %d) = %v, want %v", c.pid, c.ancestor, got, c.want)
			}
		})
	}
}

func TestPeerCredDescendsFromStopsOnACycle(t *testing.T) {
	calls := 0
	cycle := func(pid int) (int, error) {
		calls++
		if pid == 10 {
			return 20, nil
		}
		return 10, nil
	}
	if descendsFrom(10, 30, cycle) {
		t.Fatal("a parent cycle reached an ancestor outside it")
	}
	if calls > maxAncestryDepth {
		t.Fatalf("walked %d parents, want at most %d", calls, maxAncestryDepth)
	}
}
