package generation

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"google.golang.org/genai"
)

const requestTimeout = 5 * time.Minute

// GeminiGenerator owns one SDK client and creates isolated article chats.
type GeminiGenerator struct {
	client     *genai.Client
	httpClient *http.Client
	model      string
}

func NewGeminiGenerator(ctx context.Context, apiKey, model string) (*GeminiGenerator, error) {
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
	return &GeminiGenerator{client: client, httpClient: httpClient, model: model}, nil
}

func (g *GeminiGenerator) NewChat(ctx context.Context) (Generator, error) {
	chat, err := g.client.Chats.Create(ctx, g.model, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create Gemini chat: %w", err)
	}
	return &geminiChat{chat: chat, model: g.model}, nil
}

func (g *GeminiGenerator) Close() error {
	g.httpClient.CloseIdleConnections()
	return nil
}

type geminiChat struct {
	chat  *genai.Chat
	model string
}

func (c *geminiChat) Generate(ctx context.Context, prompt string) (GenerationResult, error) {
	if c.chat == nil {
		return GenerationResult{}, fmt.Errorf("Gemini chat is closed")
	}
	if strings.TrimSpace(prompt) == "" {
		return GenerationResult{}, fmt.Errorf("prompt is empty")
	}
	requestCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	var response *genai.GenerateContentResponse
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		response, err = c.chat.SendMessage(requestCtx, genai.Part{Text: prompt})
		if err == nil || !isTemporaryGeminiError(err) || attempt == 1 {
			break
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-requestCtx.Done():
			timer.Stop()
			return GenerationResult{}, fmt.Errorf("Gemini request: %w", requestCtx.Err())
		case <-timer.C:
		}
	}
	if err != nil {
		return GenerationResult{}, fmt.Errorf("Gemini request: %w", err)
	}
	text := response.Text()
	if strings.TrimSpace(text) == "" {
		return GenerationResult{}, fmt.Errorf("Gemini returned an empty response")
	}
	result := GenerationResult{Text: text, Model: c.model}
	if response.ModelVersion != "" {
		result.Model = response.ModelVersion
	}
	if response.UsageMetadata != nil {
		result.InputTokens = int(response.UsageMetadata.PromptTokenCount)
		result.OutputTokens = int(response.UsageMetadata.CandidatesTokenCount)
	}
	return result, nil
}

func (c *geminiChat) Close() error {
	c.chat = nil
	return nil
}

func isTemporaryGeminiError(err error) bool {
	var apiError genai.APIError
	if !errors.As(err, &apiError) {
		return false
	}
	return apiError.Code == http.StatusTooManyRequests || apiError.Code >= 500 && apiError.Code <= 599
}
