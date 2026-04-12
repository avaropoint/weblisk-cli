VERSION ?= dev
LDFLAGS  = -s -w -X main.version=$(VERSION)

.PHONY: build clean install test

build:
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o weblisk .

clean:
	rm -f weblisk

install:
	go install -ldflags="$(LDFLAGS)" .

test:
	go vet ./...
	go build ./...
