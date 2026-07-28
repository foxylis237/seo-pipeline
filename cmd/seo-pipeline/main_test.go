package main

import "testing"

func TestParseCommandGenerate(t *testing.T) {
	for _, test := range []struct {
		args    []string
		want    string
		wantErr bool
	}{
		{[]string{"seo-pipeline", "generate", "37"}, "generate", false},
		{[]string{"seo-pipeline", "generate"}, "", true},
		{[]string{"seo-pipeline", "generate", ""}, "", true},
		{[]string{"seo-pipeline", "generate", "37", "extra"}, "", true},
	} {
		got, err := parseCommand(test.args)
		if test.wantErr && err == nil {
			t.Fatalf("parseCommand(%v) error = nil", test.args)
		}
		if !test.wantErr && (err != nil || got != test.want) {
			t.Fatalf("parseCommand(%v) = %q, %v", test.args, got, err)
		}
	}
}
