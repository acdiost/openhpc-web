package web

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestRenderDoesNotCommitBeforeTemplateExecutionSucceeds(t *testing.T) {
	templates := template.Must(template.New("broken").Parse(`{{define "broken"}}{{.Missing}}{{end}}`))
	app := &application{templates: templates}
	e := echo.New()
	request := httptest.NewRequest(http.MethodGet, "/broken", nil)
	response := httptest.NewRecorder()
	ctx := e.NewContext(request, response)

	err := app.render(ctx, http.StatusOK, "broken", struct{}{})
	if err == nil {
		t.Fatal("render() error = nil")
	}
	if ctx.Response().Committed {
		t.Fatal("render() committed a response after template execution failed")
	}

	app.errorHandler(err, ctx)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	if body := response.Body.String(); !strings.Contains(body, "请求处理失败") {
		t.Fatalf("body = %q", body)
	}
}
