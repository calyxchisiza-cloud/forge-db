.PHONY: test race bench demo shell ui

test:
	go test ./...

race:
	go test -race ./...

bench:
	go test -bench=. -benchmem .

demo:
	go run ./cmd/demo

shell:
	go run ./cmd/shell -db forge.db

ui:
	go run ./cmd/ui
