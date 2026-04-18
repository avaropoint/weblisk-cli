package dispatch

// LLM Provider
//
// Abstraction over LLM backends. Supports OpenAI, Ollama,
// Anthropic, Cloudflare Workers AI, and OpenAI-compatible.
// Configured via WL_AI_* environment variables.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// Interface

// Provider is the interface for AI model backends.
type Provider interface {
	Chat(messages []Message) (string, error)
}

// Message is a single chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// OpenAI-compatible

// OpenAIProvider implements the Provider interface for OpenAI-compatible APIs.
type OpenAIProvider struct {
	BaseURL string
	APIKey  string
	Model   string
}

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (p *OpenAIProvider) Chat(messages []Message) (string, error) {
	body, err := json.Marshal(chatRequest{Model: p.Model, Messages: messages})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", p.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("provider request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("provider returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result chatResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parsing response: %w", err)
	}
	if result.Error != nil {
		return "", fmt.Errorf("provider error: %s", result.Error.Message)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("provider returned no choices")
	}
	return result.Choices[0].Message.Content, nil
}

// Anthropic

// AnthropicProvider implements the Provider interface for Anthropic's API.
type AnthropicProvider struct {
	BaseURL string
	APIKey  string
	Model   string
}

type anthropicRequest struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system,omitempty"`
	Messages  []Message `json:"messages"`
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (p *AnthropicProvider) Chat(messages []Message) (string, error) {
	var system string
	var apiMsgs []Message
	for _, m := range messages {
		if m.Role == "system" {
			system = m.Content
		} else {
			apiMsgs = append(apiMsgs, m)
		}
	}

	body, err := json.Marshal(anthropicRequest{
		Model: p.Model, MaxTokens: 8192, System: system, Messages: apiMsgs,
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", p.BaseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", p.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("anthropic request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("anthropic returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result anthropicResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parsing anthropic response: %w", err)
	}
	if result.Error != nil {
		return "", fmt.Errorf("anthropic error: %s", result.Error.Message)
	}

	var text strings.Builder
	for _, c := range result.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}
	return text.String(), nil
}

// Factory

// NewProvider creates a Provider from WL_AI_* environment variables.
func NewProvider() (Provider, error) {
	api := os.Getenv("WL_AI_PROVIDER")
	if api == "" {
		api = "openai"
	}
	model := os.Getenv("WL_AI_MODEL")
	baseURL := os.Getenv("WL_AI_BASE_URL")
	apiKey := os.Getenv("WL_AI_KEY")

	switch api {
	case "openai":
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		if model == "" {
			model = "gpt-4o"
		}
		if apiKey == "" {
			return nil, fmt.Errorf("WL_AI_KEY required for OpenAI")
		}
		return &OpenAIProvider{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, Model: model}, nil

	case "ollama":
		if baseURL == "" {
			baseURL = "http://localhost:11434/v1"
		}
		if model == "" {
			model = "llama3"
		}
		if apiKey == "" {
			apiKey = "ollama"
		}
		return &OpenAIProvider{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, Model: model}, nil

	case "anthropic":
		if baseURL == "" {
			baseURL = "https://api.anthropic.com/v1"
		}
		if model == "" {
			model = "claude-sonnet-4-20250514"
		}
		if apiKey == "" {
			return nil, fmt.Errorf("WL_AI_KEY required for Anthropic")
		}
		return &AnthropicProvider{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, Model: model}, nil

	case "cloudflare":
		if baseURL == "" {
			return nil, fmt.Errorf("WL_AI_BASE_URL required for Cloudflare AI")
		}
		if model == "" {
			model = "@cf/meta/llama-3-8b-instruct"
		}
		return &OpenAIProvider{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, Model: model}, nil

	default:
		if baseURL == "" {
			return nil, fmt.Errorf("WL_AI_BASE_URL required for custom provider %q", api)
		}
		if model == "" {
			model = "default"
		}
		return &OpenAIProvider{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, Model: model}, nil
	}
}

// Provider Config

// ProviderConfig describes the current AI provider configuration.
type ProviderConfig struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	BaseURL  string `json:"base_url"`
	HasKey   bool   `json:"has_key"`
}
