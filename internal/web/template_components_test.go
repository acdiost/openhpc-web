package web

import (
	"errors"
	"io/fs"
	"os"
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

	for _, markup := range []string{`<aside class="sidebar `, `<header class="topbar `} {
		if count := strings.Count(source.String(), markup); count != 1 {
			t.Errorf("%s definitions = %d, want 1", markup, count)
		}
	}
}

func TestReadableTypographyMinimums(t *testing.T) {
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

	for _, expectedUtility := range []string{
		`text-[15px]`, `text-sm`, `text-xs`, `text-2xl`,
	} {
		if !strings.Contains(source.String(), expectedUtility) {
			t.Errorf("templates do not contain the expected readable type utility %q", expectedUtility)
		}
	}
}

func TestTailwindBuildContract(t *testing.T) {
	input, err := fs.ReadFile(assets, "static/app.tailwind.css")
	if err != nil {
		t.Fatalf("read Tailwind input: %v", err)
	}
	for _, expected := range []string{`@import "tailwindcss"`, `@source "../templates"`} {
		if !strings.Contains(string(input), expected) {
			t.Errorf("Tailwind input does not contain %q", expected)
		}
	}
	if strings.Contains(string(input), "app.legacy.css") {
		t.Error("Tailwind input must not import the legacy stylesheet")
	}
	for _, forbidden := range []string{`@apply`, `@layer components`, `@media`, `{`} {
		if strings.Contains(string(input), forbidden) {
			t.Errorf("Tailwind input must not contain native component CSS %q", forbidden)
		}
	}
	if _, statErr := fs.Stat(assets, "static/app.legacy.css"); !errors.Is(statErr, fs.ErrNotExist) {
		t.Error("legacy stylesheet remains embedded in the application")
	}

	makefile, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	for _, expected := range []string{"static/app.tailwind.css", "static/app.css"} {
		if !strings.Contains(string(makefile), expected) {
			t.Errorf("Tailwind build command does not contain %q", expected)
		}
	}
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
