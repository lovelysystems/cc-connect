//go:build runas && linux

package runas

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// bridgeClient is a bridge adapter registered as platform "bridge", so
// session keys "bridge:<chat>:<user>" reach it. It keeps every message
// cc-connect sends it.
type bridgeClient struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
	mu      sync.Mutex
	msgs    []map[string]any
	stop    chan struct{}
}

func dialBridge() (*bridgeClient, error) {
	url := fmt.Sprintf("ws://127.0.0.1:%d/bridge/ws", bridgePort)
	conn, _, err := websocket.DefaultDialer.Dial(url, http.Header{"Authorization": {"Bearer " + bridgeToken}})
	if err != nil {
		return nil, err
	}
	b := &bridgeClient{conn: conn, stop: make(chan struct{})}
	if err := b.send(map[string]any{"type": "register", "platform": "bridge", "capabilities": []string{"text", "reconstruct_reply"}}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	var ack struct {
		Type string `json:"type"`
		OK   bool   `json:"ok"`
	}
	if err := conn.ReadJSON(&ack); err != nil || ack.Type != "register_ack" || !ack.OK {
		_ = conn.Close()
		return nil, fmt.Errorf("bridge register: %+v %v", ack, err)
	}
	go b.read()
	go b.ping()
	return b, nil
}

func (b *bridgeClient) send(msg map[string]any) error {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	return b.conn.WriteJSON(msg)
}

func (b *bridgeClient) read() {
	for {
		var msg map[string]any
		if err := b.conn.ReadJSON(&msg); err != nil {
			return
		}
		b.mu.Lock()
		b.msgs = append(b.msgs, msg)
		b.mu.Unlock()
	}
}

// ping keeps the connection inside cc-connect's read deadline.
func (b *bridgeClient) ping() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			_ = b.send(map[string]any{"type": "ping"})
		case <-b.stop:
			return
		}
	}
}

func (b *bridgeClient) close() {
	close(b.stop)
	_ = b.conn.Close()
}

// waitReply waits for a reply whose content is content and returns it.
func (b *bridgeClient) waitReply(t *testing.T, content string) map[string]any {
	t.Helper()
	var found map[string]any
	ok := waitFor(30*time.Second, func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		for _, m := range b.msgs {
			if m["type"] == "reply" && m["content"] == content {
				found = m
				return true
			}
		}
		return false
	})
	if !ok {
		b.mu.Lock()
		defer b.mu.Unlock()
		t.Fatalf("no bridge reply %q; got %v", content, b.msgs)
	}
	return found
}

// replyProject returns the project cc-connect named as the sender of a reply
// it addressed by session key alone.
func replyProject(t *testing.T, reply map[string]any) string {
	t.Helper()
	raw, _ := reply["reply_ctx"].(string)
	var rc struct {
		SenderProject string `json:"sender_project"`
	}
	if err := json.Unmarshal([]byte(raw), &rc); err != nil {
		t.Fatalf("reply_ctx %q: %v", raw, err)
	}
	return rc.SenderProject
}
