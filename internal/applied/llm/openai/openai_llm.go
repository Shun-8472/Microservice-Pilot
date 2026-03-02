package openai

import (
	"strings"

	llmOpenAI "github.com/tmc/langchaingo/llms/openai"

	"mini/config"
	appliedLLM "mini/internal/applied/llm"
)

type LLM struct {
}

func NewConnection() *LLM {
	return &LLM{}
}

func (l *LLM) ConnectLLM() {
	apiKey := strings.TrimSpace(config.C.LLM.APIKey)
	if apiKey == "" {
		// vLLM OpenAI-compatible endpoints typically accept any non-empty token.
		apiKey = "EMPTY"
	}

	options := []llmOpenAI.Option{
		llmOpenAI.WithToken(apiKey),
		llmOpenAI.WithModel(config.GetLLMModel()),
	}
	if baseURL := strings.TrimSpace(config.C.LLM.BaseURL); baseURL != "" {
		options = append(options, llmOpenAI.WithBaseURL(baseURL))
	}

	client, err := llmOpenAI.New(options...)
	if err != nil {
		panic(err)
	}
	appliedLLM.Connection = client
}
