// Package gemini provides the Gemini SDK transport for the shared LLM boundary.
package gemini

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/llm"
	"google.golang.org/genai"
)

const requestTimeout = 5 * time.Minute

// Client owns one SDK client and creates isolated chat sessions.
type Client struct {
	client     *genai.Client
	httpClient *http.Client
	model      string
}

// Generate implements the provider-neutral stateless LLM client.
func (c *Client) Generate(ctx context.Context, request llm.Request) (llm.Response, error) {
	if strings.TrimSpace(request.Prompt) == "" {
		return llm.Response{}, fmt.Errorf("prompt is empty")
	}
	temperature := float32(request.Temperature)
	response, err := c.client.Models.GenerateContent(ctx, request.Model, genai.Text(request.Prompt), &genai.GenerateContentConfig{
		Temperature:     &temperature,
		MaxOutputTokens: int32(request.MaxTokens),
	})
	if err != nil {
		var apiError genai.APIError
		if errors.As(err, &apiError) {
			return llm.Response{}, llm.NewStatusError(apiError.Code, apiError.Message)
		}
		return llm.Response{}, fmt.Errorf("Gemini request: %w", err)
	}
	text := response.Text()
	if strings.TrimSpace(text) == "" {
		return llm.Response{}, fmt.Errorf("Gemini returned an empty response")
	}
	result := llm.Response{Text: text, Model: request.Model}
	if response.ModelVersion != "" {
		result.Model = response.ModelVersion
	}
	if response.UsageMetadata != nil {
		result.InputTokens = int(response.UsageMetadata.PromptTokenCount)
		result.OutputTokens = int(response.UsageMetadata.CandidatesTokenCount)
	}
	return result, nil
}

func NewClient(ctx context.Context, apiKey, model string) (*Client, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("GEMINI_API_KEY is empty")
	}
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("GEMINI_MODEL is empty")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	httpClient := &http.Client{Transport: transport}
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:     apiKey,
		Backend:    genai.BackendGeminiAPI,
		HTTPClient: httpClient,
	})
	if err != nil {
		transport.CloseIdleConnections()
		return nil, fmt.Errorf("create Gemini client: %w", err)
	}
	return &Client{client: client, httpClient: httpClient, model: model}, nil
}

func (c *Client) NewChat(ctx context.Context) (llm.Chat, error) {
	chat, err := c.client.Chats.Create(ctx, c.model, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create Gemini chat: %w", err)
	}
	return &chatSession{chat: chat, model: c.model}, nil
}

func (c *Client) Close() error {
	c.httpClient.CloseIdleConnections()
	return nil
}

type chatSession struct {
	chat  *genai.Chat
	model string
}

func (c *chatSession) Generate(ctx context.Context, prompt string) (llm.Response, error) {
	if c.chat == nil {
		return llm.Response{}, fmt.Errorf("Gemini chat is closed")
	}
	if strings.TrimSpace(prompt) == "" {
		return llm.Response{}, fmt.Errorf("prompt is empty")
	}
	requestCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	var response *genai.GenerateContentResponse
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		response, err = c.chat.SendMessage(requestCtx, genai.Part{Text: prompt})
		if err == nil || !isTemporaryError(err) || attempt == 1 {
			break
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-requestCtx.Done():
			timer.Stop()
			return llm.Response{}, fmt.Errorf("Gemini request: %w", requestCtx.Err())
		case <-timer.C:
		}
	}
	if err != nil {
		return llm.Response{}, fmt.Errorf("Gemini request: %w", err)
	}
	text := response.Text()
	if strings.TrimSpace(text) == "" {
		return llm.Response{}, fmt.Errorf("Gemini returned an empty response")
	}
	result := llm.Response{Text: text, Model: c.model}
	if response.ModelVersion != "" {
		result.Model = response.ModelVersion
	}
	if response.UsageMetadata != nil {
		result.InputTokens = int(response.UsageMetadata.PromptTokenCount)
		result.OutputTokens = int(response.UsageMetadata.CandidatesTokenCount)
	}
	return result, nil
}

func (c *chatSession) Close() error {
	c.chat = nil
	return nil
}

func isTemporaryError(err error) bool {
	var apiError genai.APIError
	if !errors.As(err, &apiError) {
		return false
	}
	return apiError.Code == http.StatusTooManyRequests || apiError.Code >= 500 && apiError.Code <= 599
}
