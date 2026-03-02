package chat

import "context"

type ResponseType int

const (
	ResponseTypeUnspecified ResponseType = iota
	ResponseTypeStatus
	ResponseTypeToolResult
	ResponseTypeFinal
)

type ToolExecutionResult struct {
	ToolName   string
	ToolInput  string
	ToolOutput string
	Success    bool
}

type Request struct {
	UserInput         string
	SessionID         string
	UserID            string
	ReturnToolResults bool
}

type Response struct {
	Response   string
	SessionID  string
	TurnID     int64
	Type       ResponseType
	ToolResult []ToolExecutionResult
}

type StreamEvent struct {
	Response   string
	SessionID  string
	TurnID     int64
	Type       ResponseType
	ToolResult *ToolExecutionResult
}

type Procedure interface {
	GenerateMessage(ctx context.Context, request Request) (Response, error)
	StreamMessage(ctx context.Context, request Request, onEvent func(StreamEvent) error) error
}
