package llm

import "github.com/tmc/langchaingo/llms"

type LLM interface {
	ConnectLLM()
}

var Connection llms.Model
