VERSION ?= dev
LDFLAGS  = -s -w -X main.version=$(VERSION)

# The installed binary is named `weblisk`, not `weblisk-cli`.
#
# architecture/cli names it: every example invokes `weblisk`, and the invocation
# name is part of the interface. `go install .` names the binary after the
# module's last path element — weblisk-cli — so `make build` produced `weblisk`
# and `make install` produced `weblisk-cli`, and the two targets disagreed.
#
# Studio resolves the CLI as `weblisk` and reported "the weblisk CLI is not on
# PATH" on a machine where it was installed and working. Building to an explicit
# path is what keeps the name a decision rather than a side effect of the module
# path.
BINDIR ?= $(shell go env GOBIN)
ifeq ($(BINDIR),)
BINDIR := $(shell go env GOPATH)/bin
endif

.PHONY: build clean install test

build:
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o weblisk .

clean:
	rm -f weblisk

install:
	@mkdir -p "$(BINDIR)"
	go build -ldflags="$(LDFLAGS)" -o "$(BINDIR)/weblisk" .
	@echo "  installed $(BINDIR)/weblisk"

# Runs the tests. It used to vet and build and call that testing, which is the
# same fault CI had: nothing was enforced anywhere but by hand.
#
# WL_REQUIRE_BLUEPRINTS makes an absent corpus a failure rather than a silent
# skip, so the checks that read the blueprints are known to have run.
test:
	gofmt -l internal/ *.go | tee /dev/stderr | (! read)
	go vet ./...
	WL_REQUIRE_BLUEPRINTS=1 go test -race ./...
