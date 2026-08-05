package keysso

import "testing"

func TestResultsMatchReference(t *testing.T) {
	tests := []struct {
		name         string
		resultsURL   string
		referenceURL string
		want         bool
	}{
		{
			name:         "results page carries the requested site",
			resultsURL:   "https://www.keys.so/ru/keysbypage?base=msk&url=https%3A%2F%2Fexample.ru%2Fbarista",
			referenceURL: "https://example.ru/barista",
			want:         true,
		},
		{
			name:         "results page of another article",
			resultsURL:   "https://www.keys.so/ru/keysbypage?base=msk&url=https%3A%2F%2Fokna.ru%2Fmontazh",
			referenceURL: "https://example.ru/barista",
			want:         false,
		},
		{
			name:         "reference stored without a scheme",
			resultsURL:   "https://www.keys.so/ru/keysbypage?url=example.ru%2Fbarista",
			referenceURL: "example.ru/barista",
			want:         true,
		},
		{
			name:         "www prefix is ignored",
			resultsURL:   "https://www.keys.so/ru/keysbypage?url=example.ru%2Fbarista",
			referenceURL: "https://www.example.ru/barista",
			want:         true,
		},
		{
			name:         "empty reference cannot be checked",
			resultsURL:   "https://www.keys.so/ru/keysbypage?url=example.ru",
			referenceURL: "",
			want:         true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := resultsMatchReference(test.resultsURL, test.referenceURL); got != test.want {
				t.Fatalf("resultsMatchReference(%q, %q) = %t, want %t", test.resultsURL, test.referenceURL, got, test.want)
			}
		})
	}
}
