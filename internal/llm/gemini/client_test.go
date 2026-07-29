package gemini

import (
	"errors"
	"net/http"
	"testing"

	"google.golang.org/genai"
)

func TestTemporaryErrors(t *testing.T) {
	for _, code := range []int{http.StatusTooManyRequests, 500, 503, 599} {
		if !isTemporaryError(genai.APIError{Code: code}) {
			t.Fatalf("status %d is not temporary", code)
		}
	}
	for _, err := range []error{genai.APIError{Code: 400}, genai.APIError{Code: 404}, errors.New("network error")} {
		if isTemporaryError(err) {
			t.Fatalf("error %v is temporary", err)
		}
	}
}
