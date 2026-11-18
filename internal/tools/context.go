package tools

import "context"

type ctxKey int

const (
	callerKey ctxKey = iota
	depthKey
)

// WithCaller records which agent is executing a tool. Tools are shared by every
// agent, so a tool cannot know its caller any other way.
func WithCaller(ctx context.Context, agentName string) context.Context {
	return context.WithValue(ctx, callerKey, agentName)
}

// Caller returns the agent executing the tool, or "" if none was recorded.
func Caller(ctx context.Context) string {
	name, _ := ctx.Value(callerKey).(string)
	return name
}

// WithDelegationDepth records how many delegation hops lead to this agent.
// A top-level agent is at depth 0.
func WithDelegationDepth(ctx context.Context, depth int) context.Context {
	return context.WithValue(ctx, depthKey, depth)
}

// DelegationDepth returns the number of delegation hops above this agent.
func DelegationDepth(ctx context.Context) int {
	depth, _ := ctx.Value(depthKey).(int)
	return depth
}
