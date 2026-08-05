package generation

import (
	"context"
	"fmt"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/llm"
)

type fakeGenerator struct {
	results []llm.Response
}

func (g fakeGenerator) NewChat(context.Context, int64) (llm.Chat, error) {
	results := append([]llm.Response(nil), g.results...)
	return &fakeChat{results: results}, nil
}

type fakeChat struct {
	results []llm.Response
	next    int
}

func (c *fakeChat) Generate(_ context.Context, prompt string) (llm.Response, error) {
	if c.results == nil {
		return llm.Response{}, fmt.Errorf("fake chat is closed")
	}
	if prompt == "" {
		return llm.Response{}, fmt.Errorf("prompt is empty")
	}
	if c.next >= len(c.results) {
		return llm.Response{}, fmt.Errorf("fake generator has no result for request %d", c.next+1)
	}
	result := c.results[c.next]
	c.next++
	return result, nil
}

func (c *fakeChat) Close() error {
	c.results = nil
	return nil
}

func TestFakeGeneratorCreatesIndependentChats(t *testing.T) {
	factory := fakeGenerator{results: []llm.Response{{Text: "structure"}, {Text: "article"}}}
	first, err := factory.NewChat(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	second, err := factory.NewChat(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	for _, chat := range []llm.Chat{first, second} {
		structure, err := chat.Generate(context.Background(), "structure prompt")
		if err != nil || structure.Text != "structure" {
			t.Fatalf("structure result = %+v, %v", structure, err)
		}
		if err := chat.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := chat.Generate(context.Background(), "reused prompt"); err == nil {
			t.Fatal("closed fake chat can still be used")
		}
	}
}
