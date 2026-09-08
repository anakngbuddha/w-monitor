package dashboard

import (
	"regexp"
	"strings"
	"testing"
)

func TestBrowserAssetsFailClosedAndContainNoBearerFallback(t *testing.T) {
	html, js, err := browserAssets()
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"https://fonts.", "https://cdn.", "onclick=", "onchange=", "<script>"} {
		if strings.Contains(html, forbidden) {
			t.Errorf("served HTML contains %q", forbidden)
		}
	}
	for _, forbidden := range []string{"getApiKey(", "localStorage.setItem", "X-API-Key", "spanGaps: true", "const serverMap = {};", "const lookup = {};"} {
		if strings.Contains(js, forbidden) {
			t.Errorf("served JavaScript contains %q", forbidden)
		}
	}
	for _, required := range []string{"credentials: 'same-origin'", "AbortController", "requestID !== requestGeneration", "Object.create(null)", "Stale data", "localStorage.removeItem"} {
		if !strings.Contains(js, required) {
			t.Errorf("served JavaScript lacks %q", required)
		}
	}
	if len(regexp.MustCompile(`<script src="/[^\"]+"`).FindAllString(html, -1)) != 2 {
		t.Fatal("expected exactly two self-hosted scripts")
	}
}
