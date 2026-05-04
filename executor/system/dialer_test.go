package system_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/openotters/agentfile/executor"
	agentv1 "github.com/openotters/agentfile/executor/api/v1"
	"github.com/openotters/agentfile/executor/system"
)

// --- bufconn harness ----------------------------------------------------

// bufconnHarness wires a stub Runtime server to an in-memory
// listener and returns a system.Dialer that every subsequent Prompt /
// PromptObject / ListSessionMessages call is routed through.
//
// Cleanup registers the listener + server + resulting ClientConn on
// t.Cleanup so individual tests stay focused on behaviour assertions.
type bufconnHarness struct {
	server   *grpc.Server
	listener *bufconn.Listener
	svc      *stubAgentRuntimeServer
}

func newBufconnHarness(t *testing.T) *bufconnHarness {
	t.Helper()

	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	stub := &stubAgentRuntimeServer{}
	agentv1.RegisterAgentRuntimeServer(srv, stub)

	go func() {
		// Serve returns with an error when the listener is closed by
		// t.Cleanup; that's expected, swallow it.
		_ = srv.Serve(lis)
	}()

	t.Cleanup(func() {
		srv.GracefulStop()
		_ = lis.Close()
	})

	return &bufconnHarness{server: srv, listener: lis, svc: stub}
}

// dialer returns a system.Dialer that skips DNS/TCP entirely and
// routes every call through bufconn. Cached inside the Agent by
// a.dial, so it's only invoked once per agent.
func (h *bufconnHarness) dialer() system.Dialer {
	return system.DialerFunc(func(_ context.Context, _ string) (*grpc.ClientConn, error) {
		return grpc.NewClient("passthrough://bufnet",
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return h.listener.DialContext(ctx)
			}),
		)
	})
}

// newTestAgent returns a system.Agent pre-wired to the harness. Addr
// is non-empty because *system.Agent.Prompt* bails out when addr is
// blank; the literal value is ignored — the dialer short-circuits.
func (h *bufconnHarness) newTestAgent(t *testing.T) *system.Agent {
	t.Helper()

	return system.NewAgent(uuid.New(), memfs.New(),
		system.WithAddr("bufnet"),
		system.WithDialer(h.dialer()),
	)
}

// --- stub Runtime server ------------------------------------------

type stubAgentRuntimeServer struct {
	agentv1.UnimplementedAgentRuntimeServer

	// Responses the stub returns on each RPC. Tests set these.
	chatStreamEvents   []*agentv1.ChatStreamEvent
	chatStreamErr      error
	promptObjectResp   *agentv1.PromptObjectResponse
	promptObjectErr    error
	listSessionResp    *agentv1.ListSessionMessagesResponse
	listSessionErr     error
	lastChatStreamReq  *agentv1.ChatStreamRequest
	lastPromptObjReq   *agentv1.PromptObjectRequest
	lastListSessionReq *agentv1.ListSessionMessagesRequest
}

func (s *stubAgentRuntimeServer) ChatStream(
	req *agentv1.ChatStreamRequest, stream agentv1.AgentRuntime_ChatStreamServer,
) error {
	s.lastChatStreamReq = req

	if s.chatStreamErr != nil {
		return s.chatStreamErr
	}

	for _, ev := range s.chatStreamEvents {
		if err := stream.Send(ev); err != nil {
			return err
		}
	}

	return nil
}

func (s *stubAgentRuntimeServer) PromptObject(
	_ context.Context, req *agentv1.PromptObjectRequest,
) (*agentv1.PromptObjectResponse, error) {
	s.lastPromptObjReq = req

	if s.promptObjectErr != nil {
		return nil, s.promptObjectErr
	}

	return s.promptObjectResp, nil
}

func (s *stubAgentRuntimeServer) ListSessionMessages(
	_ context.Context, req *agentv1.ListSessionMessagesRequest,
) (*agentv1.ListSessionMessagesResponse, error) {
	s.lastListSessionReq = req

	if s.listSessionErr != nil {
		return nil, s.listSessionErr
	}

	return s.listSessionResp, nil
}

// --- guardrails: dial/prompt without addr ------------------------------

func TestPrompt_NoAddrErrors(t *testing.T) {
	t.Parallel()

	a := system.NewAgent(uuid.New(), memfs.New()) // no WithAddr

	err := a.Prompt(context.Background(), executor.PromptRequest{Prompt: "hi"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "has no runtime address") {
		t.Fatalf("Prompt without addr = %v, want 'has no runtime address' error", err)
	}
}

func TestPromptObject_NoAddrErrors(t *testing.T) {
	t.Parallel()

	a := system.NewAgent(uuid.New(), memfs.New())

	_, err := a.PromptObject(context.Background(), executor.ObjectPromptRequest{Prompt: "hi"})
	if err == nil || !strings.Contains(err.Error(), "has no runtime address") {
		t.Fatalf("PromptObject without addr = %v, want 'has no runtime address' error", err)
	}
}

func TestListSessionMessages_NoAddrErrors(t *testing.T) {
	t.Parallel()

	a := system.NewAgent(uuid.New(), memfs.New())

	_, err := a.ListSessionMessages(context.Background(), "sess", 10)
	if err == nil || !strings.Contains(err.Error(), "has no runtime address") {
		t.Fatalf("ListSessionMessages without addr = %v, want 'has no runtime address' error", err)
	}
}

// --- PromptStream ------------------------------------------------------

func TestPromptStream_DeliversEvents(t *testing.T) {
	t.Parallel()

	h := newBufconnHarness(t)
	h.svc.chatStreamEvents = []*agentv1.ChatStreamEvent{
		{Type: "step.start", Step: 1},
		{Type: "text.delta", Step: 1, Content: "hel"},
		{Type: "text.delta", Step: 1, Content: "lo"},
		{Type: "message.create", Step: 1, Content: "hello"},
	}

	a := h.newTestAgent(t)

	var got []executor.PromptEvent
	err := a.PromptStream(context.Background(),
		executor.PromptRequest{SessionID: "s1", Prompt: "hi"},
		func(ev executor.PromptEvent) { got = append(got, ev) },
	)
	if err != nil {
		t.Fatalf("PromptStream: %v", err)
	}

	if len(got) != 4 {
		t.Fatalf("received %d events, want 4", len(got))
	}

	if got[0].Type != "step.start" || got[3].Type != "message.create" {
		t.Fatalf("event order wrong: %+v", got)
	}

	// Request fields are threaded through.
	if h.svc.lastChatStreamReq.GetSessionId() != "s1" || h.svc.lastChatStreamReq.GetPrompt() != "hi" {
		t.Fatalf("request not threaded: %+v", h.svc.lastChatStreamReq)
	}
}

func TestPrompt_CapturesFinalMessage(t *testing.T) {
	t.Parallel()

	h := newBufconnHarness(t)
	h.svc.chatStreamEvents = []*agentv1.ChatStreamEvent{
		{Type: "text.delta", Content: "ignored"},
		{Type: "step.finish"},
		{Type: "message.create", Content: "the answer"},
	}

	a := h.newTestAgent(t)

	var buf bytes.Buffer
	if err := a.Prompt(context.Background(), executor.PromptRequest{Prompt: "q"}, &buf); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	if got := buf.String(); got != "the answer" {
		t.Fatalf("Prompt wrote %q, want only the final message.create content", got)
	}
}

func TestPromptStream_ServerErrorPropagates(t *testing.T) {
	t.Parallel()

	h := newBufconnHarness(t)
	h.svc.chatStreamErr = errors.New("model refused")

	a := h.newTestAgent(t)

	err := a.PromptStream(context.Background(),
		executor.PromptRequest{Prompt: "hi"},
		func(executor.PromptEvent) { t.Fatal("no events expected on error path") },
	)

	if err == nil {
		t.Fatal("expected error from server-side ChatStream failure")
	}
}

// --- PromptObject ------------------------------------------------------

func TestPromptObject_SendsSchemaAndReturnsBytes(t *testing.T) {
	t.Parallel()

	h := newBufconnHarness(t)
	h.svc.promptObjectResp = &agentv1.PromptObjectResponse{
		ObjectJson: []byte(`{"n":42}`),
	}

	a := h.newTestAgent(t)

	got, err := a.PromptObject(context.Background(), executor.ObjectPromptRequest{
		Prompt:            "guess",
		Schema:            []byte(`{"type":"object"}`),
		SchemaName:        "schema-n",
		SchemaDescription: "desc",
	})
	if err != nil {
		t.Fatalf("PromptObject: %v", err)
	}

	if string(got) != `{"n":42}` {
		t.Fatalf("got %q, want the server's object_json back", string(got))
	}

	// Every request field is threaded through.
	req := h.svc.lastPromptObjReq
	if req.GetPrompt() != "guess" ||
		string(req.GetSchemaJson()) != `{"type":"object"}` ||
		req.GetSchemaName() != "schema-n" ||
		req.GetSchemaDesc() != "desc" {
		t.Fatalf("request not fully threaded: %+v", req)
	}
}

func TestPromptObject_ServerErrorWrapped(t *testing.T) {
	t.Parallel()

	h := newBufconnHarness(t)
	h.svc.promptObjectErr = errors.New("model won't JSON")

	a := h.newTestAgent(t)

	_, err := a.PromptObject(context.Background(), executor.ObjectPromptRequest{Prompt: "x"})
	if err == nil || !strings.Contains(err.Error(), "runtime PromptObject") {
		t.Fatalf("PromptObject error = %v, want wrapped 'runtime PromptObject'", err)
	}
}

// --- ListSessionMessages -----------------------------------------------

func TestListSessionMessages_HappyPath(t *testing.T) {
	t.Parallel()

	h := newBufconnHarness(t)
	now := time.Now().Unix()
	h.svc.listSessionResp = &agentv1.ListSessionMessagesResponse{
		Messages: []*agentv1.SessionMessage{
			{Role: "user", Content: "hi", CreatedAt: now - 30},
			{Role: "assistant", Content: "hello", CreatedAt: now},
		},
	}

	a := h.newTestAgent(t)

	got, err := a.ListSessionMessages(context.Background(), "sess", 10)
	if err != nil {
		t.Fatalf("ListSessionMessages: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2", len(got))
	}

	if got[0].Role != "user" || got[1].Role != "assistant" {
		t.Fatalf("roles = %v", got)
	}

	if got[1].Content != "hello" {
		t.Fatalf("content = %q, want 'hello'", got[1].Content)
	}

	if got[1].CreatedAt.Unix() != now {
		t.Fatalf("CreatedAt = %v, want %v", got[1].CreatedAt.Unix(), now)
	}

	if h.svc.lastListSessionReq.GetSessionId() != "sess" || h.svc.lastListSessionReq.GetLimit() != 10 {
		t.Fatalf("request not threaded: %+v", h.svc.lastListSessionReq)
	}
}

func TestListSessionMessages_ServerError(t *testing.T) {
	t.Parallel()

	h := newBufconnHarness(t)
	h.svc.listSessionErr = errors.New("db down")

	a := h.newTestAgent(t)

	if _, err := a.ListSessionMessages(context.Background(), "sess", 0); err == nil {
		t.Fatal("expected server-side error to propagate")
	}
}
