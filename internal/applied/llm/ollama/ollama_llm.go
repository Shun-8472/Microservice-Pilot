package ollama

import (
	"strings"

	llmOllama "github.com/tmc/langchaingo/llms/ollama"

	"mini/config"
	appliedLLM "mini/internal/applied/llm"
)

type LLM struct {
}

func NewConnection() *LLM {
	return &LLM{}
}

func (l *LLM) ConnectLLM() {
	options := []llmOllama.Option{
		llmOllama.WithModel(config.GetLLMModel()),
	}
	if baseURL := strings.TrimSpace(config.C.LLM.BaseURL); baseURL != "" {
		options = append(options, llmOllama.WithServerURL(baseURL))
	}

	client, err := llmOllama.New(options...)

	if err != nil {
		panic(err)
	}
	appliedLLM.Connection = client
}
