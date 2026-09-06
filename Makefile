SHELL := /bin/sh
.DEFAULT_GOAL := help

# Local preflight gates. See .scratch/preflight/SPEC.md (workspace root) for
# the contract this Makefile implements, and DECISIONS.md for why.

.PHONY: help check release-check install-hooks self-test

help: ## List available targets
	@echo "Targets:"
	@echo "  check          run the cheap CI gates locally (gofmt, build, vet,"
	@echo "                 go test -race, changelog order). <60s, no network."
	@echo "  release-check  VERSION=x.y.z — the release-time changelog gate,"
	@echo "                 runnable before the tag exists."
	@echo "  install-hooks  git config core.hooksPath .githooks"
	@echo "  self-test      run this repo's checker-script tests"

# Mirrors .github/workflows/tests.yml (gofmt, build, vet, go test -race) and
# .github/workflows/changelog-hygiene.yml's push gate (order), invoked with
# the SAME command strings CI uses. self-test runs first, per the
# checker-tests rule: a broken checker script must fail as a broken checker,
# never as a false finding. scripts/preflight-parity.sh then asserts those
# command strings still match the YAML, so a drift fails loudly instead of
# quietly weakening this target.
#
# No golangci-lint here: this repo's tests.yml runs no lint step, so there is
# nothing to mirror — inventing one would make `check` diverge from CI, which
# the SPEC forbids.
check:
	@fail=0; \
	echo "==> self-test (checker scripts must pass before they gate anything)"; \
	$(MAKE) --no-print-directory self-test || { echo "FAILED: self-test (make self-test)"; fail=1; }; \
	echo "==> preflight-parity (Makefile vs CI drift check)"; \
	./scripts/preflight-parity.sh || { echo "FAILED: preflight-parity (./scripts/preflight-parity.sh)"; fail=1; }; \
	echo "==> gofmt (mirrors tests.yml: gofmt -l cmd)"; \
	unformatted=$$(gofmt -l cmd); \
	if [ -n "$$unformatted" ]; then \
		echo "$$unformatted"; gofmt -d cmd; \
		echo "FAILED: gofmt (gofmt -l cmd)"; fail=1; \
	else echo "ok"; fi; \
	echo "==> build (mirrors tests.yml: go build ./...)"; \
	go build ./... && echo ok || { echo "FAILED: build (go build ./...)"; fail=1; }; \
	echo "==> vet (mirrors tests.yml: go vet ./...)"; \
	go vet ./... && echo ok || { echo "FAILED: vet (go vet ./...)"; fail=1; }; \
	echo "==> test -race (mirrors tests.yml: go test -race ./...; measured ~22s, 2026-09-05, one package)"; \
	go test -race ./... || { echo "FAILED: test (go test -race ./...)"; fail=1; }; \
	echo "==> changelog order (mirrors changelog-hygiene.yml: ./scripts/changelog-hygiene.sh order)"; \
	./scripts/changelog-hygiene.sh order || { echo "FAILED: changelog order (./scripts/changelog-hygiene.sh order)"; fail=1; }; \
	if [ "$$fail" != "0" ]; then \
		echo ""; echo "make check: one or more gates FAILED — re-run the exact command printed above"; \
		exit 1; \
	fi; \
	echo ""; echo "make check: all gates passed"

# Mirrors release.yml's `changelog` job — the tag about to be pushed must
# already have its own CHANGELOG.md entry. release.yml only learns this AFTER
# the tag is pushed and immutable; this lets it be fixed before that point.
# The goreleaser job itself needs network + GITHUB_TOKEN and is out of scope
# for a local gate (SPEC.md D3: reuse the hygiene script, don't reimplement
# the release).
release-check:
	@if [ -z "$(VERSION)" ]; then \
		echo "usage: make release-check VERSION=x.y.z" >&2; \
		exit 2; \
	fi
	./scripts/changelog-hygiene.sh tag "v$(VERSION)"

install-hooks:
	git config core.hooksPath .githooks
	@echo "core.hooksPath -> .githooks (pre-push now runs 'make check')"

# scripts/changelog-hygiene.sh ships with no test suite of its own (it is a
# pre-existing script, not new in this pass). preflight-parity.sh IS new and
# renders a verdict, so its tests run here per the checker-tests rule.
#
# revendor-spec.sh renders a verdict too — it REFUSES to vendor the issuer spec
# from anything but a release tag, which is the only thing standing between this
# binary and a command tree generated from an unreleased spec. Its self-test
# proves the refusal still fires. The vendored copy's own integrity gate is a Go
# test (cmd/realm-id/spec_contract_test.go), already covered by `go test -race`.
self-test:
	./scripts/preflight-parity_test.sh
	./scripts/revendor-spec.sh selftest
