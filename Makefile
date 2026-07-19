TAILWINDCSS ?= tailwindcss
TAILWIND_INPUT := internal/web/static/app.tailwind.css
TAILWIND_OUTPUT := internal/web/static/app.css

.PHONY: css css-watch

css:
	$(TAILWINDCSS) --input $(TAILWIND_INPUT) --output $(TAILWIND_OUTPUT) --minify

css-watch:
	$(TAILWINDCSS) --input $(TAILWIND_INPUT) --output $(TAILWIND_OUTPUT) --watch
