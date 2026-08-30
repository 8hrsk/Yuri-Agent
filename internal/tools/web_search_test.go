package tools

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
)

type searchRoundTripFunc func(*http.Request) (*http.Response, error)

func (function searchRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestSearXNGSearchNormalizesAndBoundsResults(t *testing.T) {
	client := &http.Client{Transport: searchRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "search.example.com" || request.URL.Path != "/instance/search" || request.URL.Query().Get("q") != "yuri agent" || request.URL.Query().Get("format") != "json" || request.URL.Query().Get("language") != "ru" {
			t.Fatalf("request = %s?%s", request.URL.Path, request.URL.RawQuery)
		}
		body := `{"results":[{"title":"Первый","url":"https://example.com/one","content":"Описание","engine":"test","score":0.9},{"title":"invalid","url":"javascript:alert(1)"},{"title":"Второй","url":"https://example.org/two","content":"Ещё"}]}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewBufferString(body)), Request: request}, nil
	})}
	provider, err := NewSearXNGProvider(SearXNGConfig{Endpoint: "https://search.example.com/instance", Client: client})
	if err != nil {
		t.Fatal(err)
	}
	tool, _ := NewWebSearch(provider)
	response, err := tool.Execute(context.Background(), WebSearchRequest{Query: " yuri agent ", Limit: 3, Language: "ru"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Query != "yuri agent" || len(response.Results) != 2 || response.Results[0].Title != "Первый" || response.Results[1].Title != "Второй" {
		t.Fatalf("response = %#v", response)
	}
}

func TestWebSearchRejectsInvalidInput(t *testing.T) {
	provider, _ := NewSearXNGProvider(SearXNGConfig{Endpoint: "https://search.example.com"})
	tool, _ := NewWebSearch(provider)
	if _, err := tool.Execute(context.Background(), WebSearchRequest{}); err == nil {
		t.Fatal("empty query was accepted")
	}
	if _, err := tool.Execute(context.Background(), WebSearchRequest{Query: "x", Limit: 2}); err == nil {
		t.Fatal("undersized result limit was accepted")
	}
	if _, err := tool.Execute(context.Background(), WebSearchRequest{Query: "x", Limit: 11}); err == nil {
		t.Fatal("oversized result limit was accepted")
	}
}

func TestSearXNGProviderRejectsInsecureRemoteEndpoint(t *testing.T) {
	if _, err := NewSearXNGProvider(SearXNGConfig{Endpoint: "http://search.example.com"}); err == nil {
		t.Fatal("insecure remote endpoint was accepted")
	}
	if _, err := NewSearXNGProvider(SearXNGConfig{Endpoint: "http://localhost:8080"}); err != nil {
		t.Fatalf("localhost development endpoint rejected: %v", err)
	}
}
