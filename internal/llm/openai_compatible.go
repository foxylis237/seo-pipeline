package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// OpenAICompatibleClient implements the OpenAI chat completions protocol.
type OpenAICompatibleClient struct {
	baseURL    string
	apiKey     string
	provider   string
	httpClient *http.Client
	logger     *slog.Logger
	chatModel  string
	chatTemp   float64
	chatTokens int
}

func NewOpenAICompatibleClient(baseURL, apiKey, provider string, logger *slog.Logger) (*OpenAICompatibleClient, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("base URL is empty")
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("API key for provider %q is empty", provider)
	}
	if strings.TrimSpace(provider) == "" {
		return nil, fmt.Errorf("provider name is empty")
	}
	if logger == nil {
		return nil, fmt.Errorf("logger is nil")
	}
	return &OpenAICompatibleClient{
		baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey,
		provider: provider, httpClient: http.DefaultClient, logger: logger,
	}, nil
}

type chatCompletionRequest struct {
	Model       string                  `json:"model"`
	Messages    []chatCompletionMessage `json:"messages"`
	Temperature float64                 `json:"temperature"`
	MaxTokens   int                     `json:"max_tokens"`
}

type chatCompletionMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message chatCompletionMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func (c *OpenAICompatibleClient) Generate(ctx context.Context, request Request) (Response, error) {
	if strings.TrimSpace(request.Prompt) == "" {
		return Response{}, fmt.Errorf("prompt is empty")
	}
	return c.generateMessages(ctx, request, []chatCompletionMessage{{Role: "user", Content: request.Prompt}})
}

func (c *OpenAICompatibleClient) generateMessages(ctx context.Context, request Request, messages []chatCompletionMessage) (Response, error) {
	payload, err := json.Marshal(chatCompletionRequest{
		Model:       request.Model,
		Messages:    messages,
		Temperature: request.Temperature,
		MaxTokens:   request.MaxTokens,
	})
	if err != nil {
		return Response{}, fmt.Errorf("encode %s request: %w", c.provider, err)
	}
	if err := ctx.Err(); err != nil {
		return Response{}, fmt.Errorf("create %s HTTP request: context already done: %w", c.provider, err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return Response{}, fmt.Errorf("create %s HTTP request: %w", c.provider, err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpRequest.Header.Set("Content-Type", "application/json")

	requestStarted := time.Now()
	c.logger.Info("HTTP request started", "provider", c.provider, "model", request.Model)
	httpResponse, err := c.httpClient.Do(httpRequest)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Response{}, fmt.Errorf("execute %s HTTP request during Do: %w", c.provider, ctxErr)
		}
		return Response{}, fmt.Errorf("execute %s HTTP request during Do: %w", c.provider, err)
	}
	defer httpResponse.Body.Close()
	c.logger.Info("response headers received",
		"provider", c.provider, "model", request.Model, "status_code", httpResponse.StatusCode,
		"time_to_headers_ms", time.Since(requestStarted).Milliseconds(),
	)
	bodyReadStarted := time.Now()
	c.logger.Info("response body reading started", "provider", c.provider, "model", request.Model, "status_code", httpResponse.StatusCode)
	if httpResponse.StatusCode < http.StatusOK || httpResponse.StatusCode >= http.StatusMultipleChoices {
		body, err := io.ReadAll(io.LimitReader(httpResponse.Body, maxErrorBody))
		c.logBodyRead(request.Model, httpResponse.StatusCode, bodyReadStarted, len(body), err)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				err = ctxErr
			}
			return Response{}, fmt.Errorf("read %s HTTP error response body: %w", c.provider, err)
		}
		providerMessage := providerErrorMessage(body)
		return Response{}, NewStatusError(httpResponse.StatusCode, providerMessage)
	}

	body, err := io.ReadAll(httpResponse.Body)
	c.logBodyRead(request.Model, httpResponse.StatusCode, bodyReadStarted, len(body), err)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = ctxErr
		}
		return Response{}, fmt.Errorf("read %s HTTP response body: %w", c.provider, err)
	}
	var decoded chatCompletionResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return Response{}, fmt.Errorf("decode %s response: %w", c.provider, err)
	}
	c.logger.Info("response JSON decoded", "provider", c.provider, "model", request.Model, "status_code", httpResponse.StatusCode)
	if len(decoded.Choices) == 0 {
		return Response{}, fmt.Errorf("%s returned no choices", c.provider)
	}
	text := decoded.Choices[0].Message.Content
	if strings.TrimSpace(text) == "" {
		return Response{}, fmt.Errorf("%s returned an empty response", c.provider)
	}
	return Response{
		Text: text, Model: request.Model, InputTokens: decoded.Usage.PromptTokens,
		OutputTokens: decoded.Usage.CompletionTokens,
	}, nil
}

// ConfigureChat configures stateful Article + Info conversations for this provider.
func (c *OpenAICompatibleClient) ConfigureChat(model string, temperature float64, maxTokens int) {
	c.chatModel = strings.TrimSpace(model)
	c.chatTemp = temperature
	c.chatTokens = maxTokens
}

// NewChat starts an isolated OpenAI-compatible conversation.
func (c *OpenAICompatibleClient) NewChat(context.Context) (Chat, error) {
	if c.chatModel == "" {
		return nil, fmt.Errorf("chat model for provider %q is empty", c.provider)
	}
	return &openAICompatibleChat{client: c}, nil
}

type openAICompatibleChat struct {
	client   *OpenAICompatibleClient
	messages []chatCompletionMessage
}

func (c *openAICompatibleChat) Generate(ctx context.Context, prompt string) (Response, error) {
	if c.client == nil {
		return Response{}, fmt.Errorf("OpenAI-compatible chat is closed")
	}
	if strings.TrimSpace(prompt) == "" {
		return Response{}, fmt.Errorf("prompt is empty")
	}
	messages := append(append([]chatCompletionMessage(nil), c.messages...), chatCompletionMessage{Role: "user", Content: prompt})
	response, err := c.client.generateMessages(ctx, Request{
		Model: c.client.chatModel, Temperature: c.client.chatTemp, MaxTokens: c.client.chatTokens,
	}, messages)
	if err != nil {
		return Response{}, err
	}
	c.messages = append(messages, chatCompletionMessage{Role: "assistant", Content: response.Text})
	return response, nil
}

func (c *openAICompatibleChat) Close() error {
	c.client = nil
	c.messages = nil
	return nil
}

const maxErrorBody = 32 << 10

func (c *OpenAICompatibleClient) logBodyRead(model string, statusCode int, started time.Time, size int, err error) {
	c.logger.Info("response body read",
		"provider", c.provider, "model", model, "status_code", statusCode,
		"body_read_ms", time.Since(started).Milliseconds(), "response_size_bytes", size, "success", err == nil,
	)
}

func providerErrorMessage(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Code    any    `json:"code"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &envelope) != nil {
		return ""
	}
	message := envelope.Error.Message
	if message == "" {
		message = envelope.Message
	}
	if envelope.Error.Code != nil {
		message = fmt.Sprintf("%v %s", envelope.Error.Code, message)
	}
	return strings.TrimSpace(message)
}
