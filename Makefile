BINARY  := ktui
VERSION := 0.1.1
LDFLAGS := -s -w -X main.version=$(VERSION)
REPO    := razvanbalsan/ktui

PLATFORMS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64

.PHONY: build test vet install clean dist formula release

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

test:
	go test ./...

vet:
	go vet ./...

install: build
	install -m 0755 $(BINARY) $(HOME)/bin/$(BINARY)

# dist builds one tar.gz per platform, each containing the binary plus the
# licence and readme, and a checksums file the Homebrew formula is built from.
dist: clean
	@mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		echo "building $$os/$$arch"; \
		GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BINARY) . || exit 1; \
		tar -czf dist/$(BINARY)_$(VERSION)_$${os}_$${arch}.tar.gz -C dist $(BINARY) -C .. LICENSE README.md || exit 1; \
		rm -f dist/$(BINARY); \
	done
	@cd dist && shasum -a 256 *.tar.gz > checksums.txt
	@cat dist/checksums.txt

# formula prints the Homebrew formula for the archives in dist/, with the
# checksums filled in. Paste the output into the tap's Formula/ktui.rb.
formula:
	@sh hack/formula.sh $(VERSION) $(REPO) dist

release: test
	git tag -a v$(VERSION) -m "v$(VERSION)"
	git push origin v$(VERSION)

clean:
	rm -rf $(BINARY) dist
