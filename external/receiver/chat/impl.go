package chat

import (
	"context"

	pb "mini/external/protos/chat/v1"
	proc "mini/internal/processor/version/procedure/chat"
)

type impl struct {
	proc proc.Procedure
}

func (i impl) GenerateMessage(ctx context.Context, request *pb.ChatRequest) (*pb.ChatResponse, error) {
	responseText, err := i.proc.GenerateMessage(ctx, request.UserInput)
	if err != nil {
		return nil, err
	}

	return &pb.ChatResponse{Response: responseText}, nil
}

func (i impl) StreamMessage(request *pb.ChatRequest, stream pb.ChatService_StreamMessageServer) error {
	return i.proc.StreamMessage(stream.Context(), request.UserInput, func(chunk string) error {
		return stream.Send(&pb.ChatResponse{Response: chunk})
	})
}

func ProvideReceiver(proc proc.Procedure) Receiver {
	return &impl{
		proc: proc,
	}
}
