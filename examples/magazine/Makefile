# Run both halves of the magazine from the repository root.
#
# The API is started in the background and killed when the site exits, so "make dev"
# leaves nothing listening behind it. That matters more than it sounds: a forgotten
# background server holding port 3000 is a confusing afternoon.

ROOT := $(shell git rev-parse --show-toplevel)
API_ADDR ?= localhost:8080
SITE_PORT ?= 3000

.PHONY: dev api site test chaos

dev:
	@cd $(ROOT) && go run ./examples/magazine/cmd/api -addr $(API_ADDR) & \
	API_PID=$$!; \
	trap 'kill $$API_PID 2>/dev/null' EXIT INT TERM; \
	cd $(ROOT) && go run ./examples/magazine/cmd/site -port $(SITE_PORT) -api http://$(API_ADDR)

# The same thing with the backend misbehaving, to exercise the fallbacks.
chaos:
	@cd $(ROOT) && go run ./examples/magazine/cmd/api -addr $(API_ADDR) -fail-every 3 -latency 400ms & \
	API_PID=$$!; \
	trap 'kill $$API_PID 2>/dev/null' EXIT INT TERM; \
	cd $(ROOT) && go run ./examples/magazine/cmd/site -port $(SITE_PORT) -api http://$(API_ADDR) -log-level debug

api:
	cd $(ROOT) && go run ./examples/magazine/cmd/api -addr $(API_ADDR)

site:
	cd $(ROOT) && go run ./examples/magazine/cmd/site -port $(SITE_PORT) -api http://$(API_ADDR)

test:
	cd $(ROOT) && go test ./examples/magazine/... -race -count=1
