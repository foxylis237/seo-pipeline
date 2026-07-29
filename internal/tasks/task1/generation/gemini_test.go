package generation

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"google.golang.org/genai"
)

func TestTemporaryGeminiErrors(t *testing.T) {
	for _, code := range []int{http.StatusTooManyRequests, 500, 503, 599} {
		if !isTemporaryGeminiError(genai.APIError{Code: code}) {
			t.Fatalf("status %d is not temporary", code)
		}
	}
	for _, err := range []error{genai.APIError{Code: 400}, genai.APIError{Code: 404}, errors.New("network error")} {
		if isTemporaryGeminiError(err) {
			t.Fatalf("error %v is temporary", err)
		}
	}
}

func TestFakeGeneratorCreatesIndependentChats(t *testing.T) {
	factory := FakeGenerator{Results: []GenerationResult{{Text: "structure"}, {Text: "article"}}}
	first, err := factory.NewChat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := factory.NewChat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, chat := range []Generator{first, second} {
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
