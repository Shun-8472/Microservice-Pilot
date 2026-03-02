package chat

import (
	"context"
	"strings"

	pb "mini/external/protos/chat/v1"
	proc "mini/internal/processor/version/procedure/chat"
)

type impl struct {
	proc proc.Procedure
}

func (i impl) GenerateMessage(ctx context.Context, request *pb.ChatRequest) (*pb.ChatResponse, error) {
	response, err := i.proc.GenerateMessage(ctx, proc.Request{
		UserInput:         request.UserInput,
		SessionID:         request.SessionId,
		UserID:            request.UserId,
		ReturnToolResults: request.ReturnToolResults,
	})
	if err != nil {
		return nil, err
	}

	return toPBResponse(response, request.ReturnToolResults), nil
}

func (i impl) StreamMessage(request *pb.ChatRequest, stream pb.ChatService_StreamMessageServer) error {
	return i.proc.StreamMessage(stream.Context(), proc.Request{
		UserInput:         request.UserInput,
		SessionID:         request.SessionId,
		UserID:            request.UserId,
		ReturnToolResults: request.ReturnToolResults,
	}, func(event proc.StreamEvent) error {
		return stream.Send(toPBStreamEvent(event, request.ReturnToolResults))
	})
}

func toPBResponse(response proc.Response, returnToolResults bool) *pb.ChatResponse {
	result := &pb.ChatResponse{
		Response:      response.Response,
		SessionId:     response.SessionID,
		TurnId:        response.TurnID,
		Type:          toPBResponseType(response.Type),
		ResponseLines: toResponseLines(response.Response),
	}
	if returnToolResults {
		result.ToolResults = toPBToolResults(response.ToolResult)
	}

	return result
}

func toPBStreamEvent(event proc.StreamEvent, returnToolResults bool) *pb.ChatResponse {
	result := &pb.ChatResponse{
		Response:      event.Response,
		SessionId:     event.SessionID,
		TurnId:        event.TurnID,
		Type:          toPBResponseType(event.Type),
		ResponseLines: toResponseLines(event.Response),
	}
	if returnToolResults && event.ToolResult != nil {
		result.ToolResults = []*pb.ToolExecutionResult{
			{
				ToolName:   event.ToolResult.ToolName,
				ToolInput:  event.ToolResult.ToolInput,
				ToolOutput: event.ToolResult.ToolOutput,
				Success:    event.ToolResult.Success,
			},
		}
	}
	return result
}

func toPBToolResults(toolResults []proc.ToolExecutionResult) []*pb.ToolExecutionResult {
	if len(toolResults) == 0 {
		return nil
	}

	result := make([]*pb.ToolExecutionResult, 0, len(toolResults))
	for _, toolResult := range toolResults {
		result = append(result, &pb.ToolExecutionResult{
			ToolName:   toolResult.ToolName,
			ToolInput:  toolResult.ToolInput,
			ToolOutput: toolResult.ToolOutput,
			Success:    toolResult.Success,
		})
	}

	return result
}

func toPBResponseType(responseType proc.ResponseType) pb.ChatResponseType {
	switch responseType {
	case proc.ResponseTypeStatus:
		return pb.ChatResponseType_CHAT_RESPONSE_TYPE_STATUS
	case proc.ResponseTypeToolResult:
		return pb.ChatResponseType_CHAT_RESPONSE_TYPE_TOOL_RESULT
	case proc.ResponseTypeFinal:
		return pb.ChatResponseType_CHAT_RESPONSE_TYPE_FINAL
	default:
		return pb.ChatResponseType_CHAT_RESPONSE_TYPE_UNSPECIFIED
	}
}

func toResponseLines(text string) []string {
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	if strings.TrimSpace(normalized) == "" {
		return nil
	}

	lines := strings.Split(normalized, "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		result = append(result, trimmed)
	}

	if len(result) == 0 {
		return nil
	}
	return result
}

func ProvideReceiver(proc proc.Procedure) Receiver {
	return &impl{
		proc: proc,
	}
}
