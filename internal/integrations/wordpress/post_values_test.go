package wordpress

import "testing"

func TestSerializedStringsMatchesCheckboxValues(t *testing.T) {
	stored := `a:1:{i:0;s:4:"dist";}`
	if got, want := formatValues(serializedStrings(stored)), formatValues([]string{"dist"}); got != want {
		t.Fatalf("formatValues(serializedStrings(%q)) = %q, want %q", stored, got, want)
	}
	if got := formatValues(serializedStrings("")); got != "" {
		t.Fatalf("empty stored value gave %q, want empty", got)
	}
}
