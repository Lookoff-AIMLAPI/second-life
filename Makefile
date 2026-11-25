VERSION ?= 0.0.0-dev
DIST ?= dist
# The tray needs a desktop toolkit; everything else builds and tests anywhere.
NON_GUI = $$(go list ./... | grep -vE '/(internal/tray|cmd/slife-tray)(/|$$)')

.PHONY: help build tray check mac mac-universal windows-bin docker clean

help:
	@echo "build          $(DIST)/slife for this machine"
	@echo "tray           $(DIST)/slife-tray for this machine"
	@echo "check          gofmt, vet, tests (non-GUI packages)"
	@echo "mac            app and DMG for this Mac's architecture"
	@echo "mac-universal  app and DMG for Apple silicon and Intel"
	@echo "windows-bin    slife.exe and slife-tray.exe (the installer itself: packaging/windows/build.ps1)"
	@echo "docker         container image"
	@echo "clean          remove $(DIST)"

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/Lookoff-AIMLAPI/second-life/internal/bridge.Version=$(VERSION)" -o $(DIST)/slife ./cmd/slife

tray:
	go build -trimpath -ldflags "-s -w -X github.com/Lookoff-AIMLAPI/second-life/internal/bridge.Version=$(VERSION)" -o $(DIST)/slife-tray ./cmd/slife-tray

check:
	@files="$$(gofmt -l .)"; if [ -n "$$files" ]; then echo "gofmt needed:"; echo "$$files"; exit 1; fi
	go vet $(NON_GUI)
	go test $(NON_GUI)

mac:
	VERSION=$(VERSION) packaging/mac/build.sh

mac-universal:
	UNIVERSAL=1 VERSION=$(VERSION) packaging/mac/build.sh

windows-bin:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/Lookoff-AIMLAPI/second-life/internal/bridge.Version=$(VERSION)" -o $(DIST)/windows-amd64/slife.exe ./cmd/slife
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/Lookoff-AIMLAPI/second-life/internal/bridge.Version=$(VERSION) -H windowsgui" -o $(DIST)/windows-amd64/slife-tray.exe ./cmd/slife-tray

docker:
	VERSION=$(VERSION) docker compose build

clean:
	rm -rf dist
