//go:build integration

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"kota/assets"
	"kota/internal/docker"
	"kota/internal/models"
	"kota/internal/pi"
	"kota/internal/sandbox"
)

// scriptedGateway is a model gateway that lists models.yaml's default and
// answers each chat request with the next of replies, streamed, recording
// the requests.
type scriptedGateway struct {
	mu       sync.Mutex
	replies  []string
	requests []chatRequest
}

// chatRequest is the part of a chat completion request the tests read.
type chatRequest struct {
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	Tools []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools"`
}

// text is every message's content, for matching.
func (r chatRequest) text() string {
	var b strings.Builder
	for _, m := range r.Messages {
		fmt.Fprintf(&b, "[%s] %s\n", m.Role, m.Content)
	}
	return b.String()
}

// toolCallReply is a streamed reply calling one tool.
func toolCallReply(name string, args map[string]string) string {
	a, _ := json.Marshal(args)
	call := map[string]any{"index": 0, "id": "call_1", "type": "function", "function": map[string]any{"name": name, "arguments": string(a)}}
	return chunk(map[string]any{"role": "assistant", "tool_calls": []any{call}}, nil) + chunk(map[string]any{}, "tool_calls")
}

// textReply is a streamed reply ending the turn with text.
func textReply(text string) string {
	return chunk(map[string]any{"role": "assistant", "content": text}, nil) + chunk(map[string]any{}, "stop")
}

func chunk(delta map[string]any, finish any) string {
	data, _ := json.Marshal(map[string]any{
		"id": "chatcmpl-1", "object": "chat.completion.chunk", "created": 0, "model": "m",
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
	})
	return "data: " + string(data) + "\n\n"
}

// start serves g on host loopback and returns its URL as the sandbox sees it.
func (g *scriptedGateway) start(t *testing.T) string {
	t.Helper()
	f, err := models.ParseFile(assets.Models)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"data":[{"id":%q}]}`, f.Default)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req chatRequest
		json.Unmarshal(body, &req)
		g.mu.Lock()
		g.requests = append(g.requests, req)
		reply := textReply("nothing more scripted")
		if len(g.replies) > 0 {
			reply, g.replies = g.replies[0], g.replies[1:]
		}
		g.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, reply+"data: [DONE]\n\n")
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return "http://host.docker.internal:" + strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
}

// TestTicketDone runs pi in a shell Session against a scripted gateway. A
// ticket_done call replaces the conversation with the fixed note, with no
// summary request to the model; a bad call returns an error and clears
// nothing; and the extension can't be changed.
func TestTicketDone(t *testing.T) {
	requireBaseImage(t)
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(config, "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("KOTA_GATEWAY_KEY", "")
	t.Setenv("KOTA_GATEWAY_KEY_FILE", "")
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(cache, "kota-test-")
	if err != nil {
		t.Fatal(err)
	}
	root, _ = filepath.EvalSymlinks(root)
	t.Cleanup(func() { os.RemoveAll(root) })
	t.Setenv("KOTA_ALLOWED_ROOT", root)
	project := filepath.Join(root, "app")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	hostGit(t, project, "init", "-q")

	cases := []struct {
		name   string
		ticket string
		// wantErr is the tool error a bad call returns; "" means the call
		// clears the conversation.
		wantErr string
	}{
		{name: "done", ticket: ".scratch/f/issues/01-a.md"},
		{name: "number", ticket: "7"},
		{name: "missing argument", ticket: "  ", wantErr: "both required"},
		{name: "outside the workspace", ticket: "/etc/passwd", wantErr: "under /workspace"},
		{name: "escapes the workspace", ticket: "../x.md", wantErr: "under /workspace"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := &scriptedGateway{replies: []string{
				toolCallReply("ticket_done", map[string]string{"ticket": c.ticket, "commit": "abc1234"}),
				textReply("taking the next ticket"),
			}}
			sa := sessionArgs{agent: pi.Agent, gatewayURL: g.start(t), dir: project, shell: true}
			var out string
			withSession(t, sa, func(*sandbox.Compose) {}, func(sess *session, ds *docker.Session) {
				out, err = ds.Capture("bash", "-c", `exec 2>&1
ext=$1/ticket-done.ts
pi -p 'START-MARKER: work on the tickets' </dev/null
echo "PI-EXIT=$?"
echo x >> "$ext" && echo WROTE
touch "$1/evil.ts" && echo ADDED
true`, "bash", sess.agent.extensions)
				if err != nil {
					t.Fatalf("%v\n%s", err, out)
				}
			})
			t.Logf("inside:\n%s", out)
			for _, bad := range []string{"WROTE", "ADDED"} {
				if strings.Contains(out, bad) {
					t.Errorf("the agent could change its extensions: %s", bad)
				}
			}
			g.mu.Lock()
			defer g.mu.Unlock()
			if len(g.requests) != 2 {
				t.Fatalf("%d chat requests, want 2 (no summary request):\n%+v", len(g.requests), g.requests)
			}
			var listed bool
			for _, tool := range g.requests[0].Tools {
				listed = listed || tool.Function.Name == "ticket_done"
			}
			if !listed {
				t.Errorf("ticket_done isn't offered to the model: %+v", g.requests[0].Tools)
			}
			if !strings.Contains(g.requests[0].text(), "call the ticket_done tool") {
				t.Errorf("pi's instructions don't mention ticket_done:\n%s", g.requests[0].text())
			}
			after := g.requests[1].text()
			t.Logf("second request, without the system prompt:\n%s", g.requests[1].conversation())
			if c.wantErr == "" {
				if strings.Contains(after, "START-MARKER") || strings.Contains(after, "call_1") {
					t.Errorf("the conversation wasn't cleared:\n%s", after)
				}
				want := "Ticket " + c.ticket + " is done in commit abc1234. The previous conversation was cleared."
				if !strings.Contains(after, want) || !strings.Contains(after, "work unattended, that still applies") {
					t.Errorf("second request lacks the fixed note %q:\n%s", want, after)
				}
				return
			}
			if !strings.Contains(after, "START-MARKER") || !strings.Contains(after, c.wantErr) {
				t.Errorf("a bad call cleared the conversation or wasn't an error:\n%s", after)
			}
			if strings.Contains(after, "The previous conversation was cleared") {
				t.Errorf("a bad call compacted:\n%s", after)
			}
		})
	}
}

// conversation is text without the system prompt, for logs.
func (r chatRequest) conversation() string {
	var b strings.Builder
	for _, m := range r.Messages {
		if m.Role != "system" {
			fmt.Fprintf(&b, "[%s] %s\n", m.Role, m.Content)
		}
	}
	return b.String()
}
