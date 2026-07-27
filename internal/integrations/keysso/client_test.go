package keysso

import (
	"reflect"
	"testing"
)

func TestParseQueriesPreservesOrderAndRemovesEmptyLines(t *testing.T) {
	got := parseQueries("  первый запрос\r\n\r\nвторой запрос  \n третий запрос ")
	want := []string{"первый запрос", "второй запрос", "третий запрос"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseQueries() = %#v, want %#v", got, want)
	}
}

func TestNormalizeQueriesDoesNotSortOrDeduplicate(t *testing.T) {
	got := normalizeQueries([]string{"второй", "первый", "второй"})
	want := []string{"второй", "первый", "второй"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeQueries() = %#v, want %#v", got, want)
	}
}

func TestSamePageIgnoresTrailingSlashAndQuery(t *testing.T) {
	if !samePage(
		"https://www.keys.so/ru/?source=profile",
		"https://www.keys.so/ru/",
	) {
		t.Fatal("expected URLs to identify the same Keys.so page")
	}
}

func TestSamePageRejectsDifferentPath(t *testing.T) {
	if samePage(
		"https://www.keys.so/ru/login",
		"https://www.keys.so/ru/",
	) {
		t.Fatal("different Keys.so paths must not be treated as the same page")
	}
}
