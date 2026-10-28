package server

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jerryctt/peerbridge/internal/exec"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/rs/zerolog"
)

// rpc sends one JSON-RPC message through the server and decodes the result
// into out via a JSON round-trip (independent of mcp-go's concrete types).
func rpc(t *testing.T, s interface {
	HandleMessage(context.Context, json.RawMessage) mcp.JSONRPCMessage
}, msg string, out any) {
	t.Helper()
	raw, err := json.Marshal(s.HandleMessage(context.Background(), json.RawMessage(msg)))
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Error) > 0 {
		t.Fatalf("rpc error: %s", env.Error)
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		t.Fatalf("decode result: %v (%s)", err, env.Result)
	}
}

func toolText(res mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

func TestRegisteredTools(t *testing.T) {
	cfg, _ := newCfg(t)
	s, shutdown := New(cfg, &fakeRunner{}, zerolog.Nop())
	defer shutdown()

	var list mcp.ListToolsResult
	rpc(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, &list)

	var names []string
	byName := map[string]mcp.Tool{}
	for _, tl := range list.Tools {
		names = append(names, tl.Name)
		byName[tl.Name] = tl
	}
	sort.Strings(names)
	want := "chat_start,codex_review_start,codex_review_status,job_status,review_start"
	if got := strings.Join(names, ","); got != want {
		t.Errorf("tools = %s\nwant    %s", got, want)
	}
	for _, alias := range []string{"codex_review_start", "codex_review_status"} {
		if !strings.Contains(byName[alias].Description, "Deprecated") {
			t.Errorf("%s description should say Deprecated", alias)
		}
	}
	if _, ok := byName["review_start"].InputSchema.Properties["agent"]; !ok {
		t.Error("review_start must take an agent param")
	}
	if _, ok := byName["chat_start"].InputSchema.Properties["session_id"]; !ok {
		t.Error("chat_start must take a session_id param")
	}
	if _, ok := byName["codex_review_start"].InputSchema.Properties["agent"]; ok {
		t.Error("codex_review_start alias must not expose an agent param")
	}
}

func TestAliasToolRunsCodexReview(t *testing.T) {
	cfg, repo := newCfg(t)
	s, shutdown := New(cfg, &fakeRunner{reply: map[string]exec.Result{
		"codex review --uncommitted": {Stdout: "ALIAS_OK"},
	}}, zerolog.Nop())
	defer shutdown()

	var start mcp.CallToolResult
	rpc(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"codex_review_start","arguments":{"folder":`+strconv.Quote(repo)+`}}}`, &start)
	text := toolText(start)
	if start.IsError || !strings.Contains(text, "job_id: ") || !strings.Contains(text, "agent: codex") {
		t.Fatalf("alias start = %q isError=%v", text, start.IsError)
	}
	id := jobID(t, text)

	deadline := time.Now().Add(5 * time.Second)
	for {
		var status mcp.CallToolResult
		rpc(t, s, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"codex_review_status","arguments":{"job_id":"`+id+`"}}}`, &status)
		text = toolText(status)
		if strings.Contains(text, "status: completed") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never completed: %q", text)
		}
	}
	if !strings.Contains(text, "ALIAS_OK") {
		t.Errorf("alias status = %q", text)
	}
}

func TestChatStartWithoutAgentOrClientInfoIsToolError(t *testing.T) {
	cfg, repo := newCfg(t)
	s, shutdown := New(cfg, &fakeRunner{}, zerolog.Nop())
	defer shutdown()

	var res mcp.CallToolResult
	rpc(t, s, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"chat_start","arguments":{"folder":`+strconv.Quote(repo)+`,"message":"hi"}}}`, &res)
	if !res.IsError || !strings.Contains(toolText(res), "specify agent") {
		t.Errorf("want isError with 'specify agent', got %q isError=%v", toolText(res), res.IsError)
	}
}

func TestReadOnlyAnnotations(t *testing.T) {
	cfg, _ := newCfg(t)
	cfg.StatusPoll = 100 * time.Second
	s, shutdown := New(cfg, &fakeRunner{}, zerolog.Nop())
	defer shutdown()
	var list mcp.ListToolsResult
	rpc(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, &list)
	for _, tl := range list.Tools {
		ro := tl.Annotations.ReadOnlyHint
		isRO := ro != nil && *ro
		if !isRO {
			t.Errorf("%s should be marked readOnlyHint", tl.Name)
		}
		if tl.Name == "job_status" && !strings.Contains(tl.Description, "~1m40s") {
			t.Errorf("job_status description should reflect status_poll: %q", tl.Description)
		}
	}
}
