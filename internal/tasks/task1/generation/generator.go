// Package generation defines the article text generation boundary.
package generation

import (
	"context"
	"fmt"
)

// Generator generates text without exposing a concrete LLM SDK to the pipeline.
type Generator interface {
	Generate(ctx context.Context, prompt string) (GenerationResult, error)
	Close() error
}

// ChatFactory creates an isolated conversation for one article.
type ChatFactory interface {
	NewChat(ctx context.Context) (Generator, error)
}

// GenerationResult contains generated text and provider-neutral usage data.
type GenerationResult struct {
	Text         string
	Model        string
	InputTokens  int
	OutputTokens int
}

// FakeGenerator is a local generator used until an LLM integration is connected.
type FakeGenerator struct {
	Result  GenerationResult
	Results []GenerationResult
	Err     error
}

func (g FakeGenerator) Generate(_ context.Context, _ string) (GenerationResult, error) {
	return g.Result, g.Err
}

func (g FakeGenerator) Close() error { return nil }

func (g FakeGenerator) NewChat(_ context.Context) (Generator, error) {
	results := g.Results
	if len(results) == 0 {
		results = []GenerationResult{g.Result, g.Result, g.Result}
	}
	return &fakeChat{results: results, err: g.Err}, nil
}

type fakeChat struct {
	results []GenerationResult
	err     error
	next    int
}

func (c *fakeChat) Generate(_ context.Context, prompt string) (GenerationResult, error) {
	if c.results == nil {
		return GenerationResult{}, fmt.Errorf("fake chat is closed")
	}
	if prompt == "" {
		return GenerationResult{}, fmt.Errorf("prompt is empty")
	}
	if c.err != nil {
		return GenerationResult{}, c.err
	}
	if c.next >= len(c.results) {
		return GenerationResult{}, fmt.Errorf("fake generator has no result for request %d", c.next+1)
	}
	result := c.results[c.next]
	c.next++
	return result, nil
}

func (c *fakeChat) Close() error {
	c.results = nil
	return nil
}
