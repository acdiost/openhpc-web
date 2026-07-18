package web

import (
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestApplicationChromeMarkupHasSingleDefinition(t *testing.T) {
	templatePaths, err := fs.Glob(assets, "templates/*.html")
	if err != nil {
		t.Fatalf("glob templates: %v", err)
	}

	var source strings.Builder
	for _, path := range templatePaths {
		contents, readErr := fs.ReadFile(assets, path)
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		source.Write(contents)
	}

	for _, markup := range []string{`<aside class="sidebar"`, `<header class="topbar"`} {
		if count := strings.Count(source.String(), markup); count != 1 {
			t.Errorf("%s definitions = %d, want 1", markup, count)
		}
	}
}

func TestReadableTypographyMinimums(t *testing.T) {
	contents, err := fs.ReadFile(assets, "static/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	stylesheet := string(contents)

	assertFontSizeAtLeast(t, stylesheet, `:root`, 18)
	assertFontSizeAtLeast(t, stylesheet, `.nav-item`, 14)
	assertFontSizeAtLeast(t, stylesheet, `.data-table td`, 14)
	assertFontSizeAtLeast(t, stylesheet, `.login-form label`, 14)
	assertFontSizeAtLeast(t, stylesheet, `.page-heading h1`, 30)
}

func TestMobileMenuScriptSynchronizesExpandedState(t *testing.T) {
	contents, err := fs.ReadFile(assets, "static/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	script := string(contents)
	for _, expected := range []string{`setAttribute('aria-expanded'`, `event.key === 'Escape'`} {
		if !strings.Contains(script, expected) {
			t.Errorf("app.js does not contain %q", expected)
		}
	}
}

func TestJobResourceScriptPollsAndCancelsWithModalLifecycle(t *testing.T) {
	contents, err := fs.ReadFile(assets, "static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(contents)
	for _, expected := range []string{
		"[data-job-resource]", "/resources", "setTimeout", "clearTimeout", "AbortController", "data-resource-chart",
	} {
		if !strings.Contains(script, expected) {
			t.Errorf("app.js does not contain %q", expected)
		}
	}
}

func assertFontSizeAtLeast(t *testing.T, stylesheet, selector string, minimum int) {
	t.Helper()
	pattern := regexp.MustCompile(regexp.QuoteMeta(selector) + `[^{}]*\{[^{}]*font-size:([0-9]+)px`)
	match := pattern.FindStringSubmatch(stylesheet)
	if len(match) != 2 {
		t.Fatalf("%s has no pixel font-size declaration", selector)
	}
	size, err := strconv.Atoi(match[1])
	if err != nil {
		t.Fatalf("parse %s font size %q: %v", selector, match[1], err)
	}
	if size < minimum {
		t.Errorf("%s font-size = %dpx, want at least %dpx", selector, size, minimum)
	}
}
