package applied

import (
	"strings"

	"mini/config"
	"mini/internal/applied/llm"
	"mini/internal/applied/llm/ollama"
	"mini/internal/applied/llm/openai"
)

func InitLLMConnection() llm.LLM {
	provider := strings.ToLower(config.GetLLMProvider())
	if provider == "openai" || provider == "vllm" {
		return openai.NewConnection()
	}

	return ollama.NewConnection()
}
