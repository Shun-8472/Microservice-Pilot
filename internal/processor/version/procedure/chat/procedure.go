package chat

import "context"

type Procedure interface {
	GenerateMessage(ctx context.Context, userInput string) (string, error)
	StreamMessage(ctx context.Context, userInput string, onChunk func(string) error) error
}
