// Package trace records agent trajectories (runs, LLM calls, tool calls) into memory.Store.
//
// A run is started with Start and carried in the context. The Engine and Tool decorators log
// a step whenever they are called with a run in the context, so the agent loop itself needs no
// changes and any nested run (delegation, swarm workers) links to its parent automatically.
// Tracing never fails the work it observes: storage errors are logged and swallowed.
package trace

import (
	"context"
	"encoding/json"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/chengyaolee/blooper/internal/llm"
	"github.com/chengyaolee/blooper/internal/memory"
	"github.com/chengyaolee/blooper/internal/tools"
	"github.com/google/uuid"
)

// Run is an agent run being recorded.
type Run struct {
	ID    string
	store *memory.Store
}

type ctxKey struct{}

// FromContext returns the run carried by ctx, or nil.
func FromContext(ctx context.Context) *Run {
	r, _ := ctx.Value(ctxKey{}).(*Run)
	return r
}

// Info describes a run when it starts.
type Info struct {
	Agent  string
	Role   string // chat | plan | work | verify | synthesize | delegate
	Model  string
	GoalID string
	TaskID string
	Input  string
	Config any // snapshot of the agent config, stored once by content hash
}

// Start records a new run (as a child of the run in ctx, if any) and returns a context carrying it.
// With a nil store it is a no-op returning a nil *Run, whose methods are all safe to call.
func Start(ctx context.Context, store *memory.Store, info Info) (context.Context, *Run) {
	if store == nil {
		return ctx, nil
	}
	r := &Run{ID: uuid.NewString(), store: store}
	ri := memory.RunInfo{ID: r.ID, GoalID: info.GoalID, TaskID: info.TaskID, Agent: info.Agent,
		Role: info.Role, Model: info.Model, Input: info.Input}
	if parent := FromContext(ctx); parent != nil {
		ri.ParentID = parent.ID
	}
	if info.Config != nil {
		ri.ConfigHash = report(store.PutBlob("agent_config", info.Config))
	}
	if err := store.StartRun(ri); err != nil {
		log.Printf("trace: start run: %v", err)
		return ctx, nil
	}
	return context.WithValue(ctx, ctxKey{}, r), r
}

// End records the run's final output or error.
func (r *Run) End(output string, err error) {
	if r == nil {
		return
	}
	if e := r.store.EndRun(r.ID, output, err); e != nil {
		log.Printf("trace: end run: %v", e)
	}
}

// Actor returns the task-graph actor for an agent acting within this run.
func (r *Run) Actor(agent string) memory.Actor {
	if r == nil {
		return memory.Actor{Agent: agent}
	}
	return memory.Actor{Agent: agent, RunID: r.ID}
}

// RunID returns the run ID, or "" for a nil run.
func (r *Run) RunID() string {
	if r == nil {
		return ""
	}
	return r.ID
}

func (r *Run) log(st memory.Step) {
	st.RunID = r.ID
	if err := r.store.LogStep(st); err != nil {
		log.Printf("trace: log %s step: %v", st.Kind, err)
	}
}

func report(s string, err error) string {
	if err != nil {
		log.Printf("trace: %v", err)
	}
	return s
}

// Engine wraps an inference engine so every call made inside a run is recorded.
func Engine(inner llm.InferenceEngine) llm.InferenceEngine {
	return engine{inner}
}

type engine struct{ llm.InferenceEngine }

// requestInput is exactly what the model saw, with the (large, static) tool schemas stored once.
func (r *Run) requestInput(req *llm.ChatRequest) map[string]any {
	defs := slices.Clone(req.Tools)
	slices.SortFunc(defs, func(a, b llm.ToolDef) int { return strings.Compare(a.Function.Name, b.Function.Name) })
	in := map[string]any{"model": req.Model, "temperature": req.Temperature, "messages": req.Messages}
	if req.MaxTokens != 0 {
		in["max_tokens"] = req.MaxTokens
	}
	if len(defs) > 0 {
		in["tools_hash"] = report(r.store.PutBlob("tool_schemas", defs))
	}
	return in
}

func (e engine) Generate(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	r := FromContext(ctx)
	if r == nil {
		return e.InferenceEngine.Generate(ctx, req)
	}
	start := time.Now()
	resp, err := e.InferenceEngine.Generate(ctx, req)
	st := memory.Step{Kind: "llm", Name: req.Model, Input: r.requestInput(req), LatencyMS: time.Since(start).Milliseconds()}
	if err != nil {
		st.Error = err.Error()
	} else {
		st.Output = resp.Message
		st.PromptTokens, st.CompletionTokens = resp.Usage.PromptTokens, resp.Usage.CompletionTokens
	}
	r.log(st)
	return resp, err
}

func (e engine) Stream(ctx context.Context, req *llm.ChatRequest, out chan<- llm.StreamChunk) error {
	r := FromContext(ctx)
	if r == nil {
		return e.InferenceEngine.Stream(ctx, req, out)
	}
	start := time.Now()
	mid := make(chan llm.StreamChunk, cap(out))
	msg := llm.ChatMessage{Role: "assistant"}
	var content strings.Builder
	var usage llm.Usage
	var streamErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(out)
		for c := range mid {
			content.WriteString(c.Content)
			msg.ToolCalls = append(msg.ToolCalls, c.ToolCalls...)
			if c.Done {
				usage = c.Usage
			}
			if c.Error != nil {
				streamErr = c.Error
			}
			select {
			case out <- c:
			case <-ctx.Done(): // keep draining so the inner engine never blocks
			}
		}
	}()
	err := e.InferenceEngine.Stream(ctx, req, mid) // closes mid
	<-done
	msg.Content = content.String()
	st := memory.Step{Kind: "llm", Name: req.Model, Input: r.requestInput(req), Output: msg,
		LatencyMS: time.Since(start).Milliseconds(), PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens}
	if err == nil {
		err = streamErr
	}
	if err != nil {
		st.Error = err.Error()
	}
	r.log(st)
	return err
}

// Tool wraps a tool so every execution inside a run is recorded. Wrapping is idempotent.
func Tool(t tools.Tool) tools.Tool {
	if _, ok := t.(tracedTool); ok {
		return t
	}
	return tracedTool{t}
}

// WrapRegistry replaces every tool in reg with its traced version.
func WrapRegistry(reg *tools.ToolRegistry) {
	for _, t := range reg.List() {
		reg.Register(Tool(t))
	}
}

type tracedTool struct{ tools.Tool }

func (t tracedTool) Execute(ctx context.Context, args json.RawMessage) (*tools.ToolResult, error) {
	r := FromContext(ctx)
	if r == nil {
		return t.Tool.Execute(ctx, args)
	}
	start := time.Now()
	res, err := t.Tool.Execute(ctx, args)
	var in any = args
	if !json.Valid(args) {
		in = string(args)
	}
	st := memory.Step{Kind: "tool", Name: t.Name(), Input: in, Output: res, LatencyMS: time.Since(start).Milliseconds()}
	switch {
	case err != nil:
		st.Error = err.Error()
	case res != nil && res.Error != "":
		st.Error = res.Error
	}
	r.log(st)
	return res, err
}
