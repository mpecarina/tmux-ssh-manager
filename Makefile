.DEFAULT_GOAL := all

.PHONY: fmt test build smoke all clean

fmt:
	@./scripts/harness.sh fmt

test:
	@./scripts/harness.sh test

build:
	@./scripts/harness.sh build

smoke:
	@./scripts/harness.sh smoke

all:
	@./scripts/harness.sh all

clean:
	rm -rf bin/
