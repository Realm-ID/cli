package main

// Integrity gate for the vendored issuer contract.
//
// WHY THIS EXISTS. This CLI has no SDK dependency: openapi.yaml IS the source
// of every command, flag and argument — buildCommands() derives the whole tree
// from it at startup (ADR-062 §1). A wrong vendor therefore does not produce a
// wrong document, it produces a wrong PROGRAM. Until 2026-09-06 the file was
// re-synced with `//go:generate cp ../../../issuer/docs/swagger.yaml`, a copy
// from the sibling WORKING TREE: it could vendor an unreleased, mid-edit or
// dirty spec, and nothing anywhere would notice. It is now vendored from a
// release tag by scripts/revendor-spec.sh, and ISSUER_CONTRACT records which.
//
// This test is what makes that record load-bearing rather than decorative.
//
// It does NOT check the fixtures-vs-spec property that Realm-ID/api's
// contract_parity_test.go checks — this repo has no issuer fake. What it
// checks is provenance and non-vacuity: the embedded bytes are the bytes
// ISSUER_CONTRACT names, and a tree of real size still comes out of them.
//
// The CROSS-REPO half — that this pin and the BFF's pin name the SAME issuer
// release — cannot live here: neither repo can see the other. The umbrella's
// scripts/issuer-pin-parity.py enforces it. See DECISIONS.md 2026-09-06.

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

func readIssuerContract(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile("ISSUER_CONTRACT")
	if err != nil {
		t.Fatalf("ISSUER_CONTRACT is unreadable (%v).\n"+
			"It records which issuer release openapi.yaml came from. This gate does not skip on a\n"+
			"missing input — re-vendor with ../../scripts/revendor-spec.sh vX.Y.Z", err)
	}
	meta := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("ISSUER_CONTRACT: unparseable line %q", line)
		}
		meta[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return meta
}

// TestVendoredSpecMatchesItsProvenance is the integrity half: the embedded
// bytes must be the bytes ISSUER_CONTRACT names. A hand-edit of either file
// fails here rather than being absorbed.
func TestVendoredSpecMatchesItsProvenance(t *testing.T) {
	meta := readIssuerContract(t)

	for _, k := range []string{"issuer_tag", "issuer_repo", "spec_info_version", "spec_sha256"} {
		if meta[k] == "" {
			t.Fatalf("ISSUER_CONTRACT: %s is missing or empty", k)
		}
	}
	// The pin must be a RELEASE TAG. A branch name or a bare version here would
	// mean someone bypassed revendor-spec.sh, which is the whole guarantee.
	tag := meta["issuer_tag"]
	if !strings.HasPrefix(tag, "v") || strings.Count(tag, ".") != 2 || strings.ContainsAny(tag, "/ ") {
		t.Errorf("ISSUER_CONTRACT: issuer_tag=%q is not a release tag (vX.Y.Z). "+
			"A branch, HEAD or a path describes an issuer nobody is running", tag)
	}

	if len(openapiYAML) == 0 {
		t.Fatal("the embedded openapi.yaml is EMPTY — every command in this binary is derived from it")
	}
	sum := sha256.Sum256(openapiYAML)
	got := hex.EncodeToString(sum[:])
	if got != meta["spec_sha256"] {
		t.Fatalf("the embedded spec is not the one ISSUER_CONTRACT names.\n"+
			"  ISSUER_CONTRACT says sha256=%s (issuer %s)\n"+
			"  openapi.yaml hashes to %s\n"+
			"Neither file is hand-editable: re-vendor with ../../scripts/revendor-spec.sh vX.Y.Z",
			meta["spec_sha256"], tag, got)
	}

	// And the recorded info.version must actually be the spec's own, so the two
	// halves of the provenance cannot drift apart silently.
	var infoVersion string
	inInfo := false
	for _, line := range strings.Split(string(openapiYAML), "\n") {
		if strings.HasPrefix(line, "info:") {
			inInfo = true
			continue
		}
		if inInfo {
			if v, ok := strings.CutPrefix(line, "  version:"); ok {
				infoVersion = strings.TrimSpace(v)
				break
			}
			if len(line) > 0 && line[0] != ' ' && line[0] != '#' {
				break
			}
		}
	}
	if infoVersion == "" {
		t.Fatal("could not read info.version out of the embedded spec")
	}
	if infoVersion != meta["spec_info_version"] {
		t.Errorf("ISSUER_CONTRACT says spec_info_version=%s but the embedded spec says %s",
			meta["spec_info_version"], infoVersion)
	}
	t.Logf("issuer contract: %s (%s, info.version %s, sha256 %s…)",
		tag, meta["issuer_repo"], infoVersion, got[:12])
}

// TestVendoredSpecStillBuildsACommandTree is the non-vacuity half. The hash
// check above would pass just as happily on a valid-but-gutted spec, and this
// binary's entire surface is derived from it — so assert a tree of real size
// still comes out, and that the landmark groups are in it.
func TestVendoredSpecStillBuildsACommandTree(t *testing.T) {
	cmds, _, err := buildCommands()
	if err != nil {
		t.Fatalf("buildCommands() failed against the vendored spec: %v", err)
	}
	// The tree at issuer v0.121.1 is well over a hundred commands. The floor is
	// deliberately far below that: this catches a gutted or truncated vendor,
	// not a normal release-to-release swing.
	const floor = 50
	if len(cmds) < floor {
		t.Fatalf("the vendored spec yields only %d commands (floor %d) — that is a truncated "+
			"or gutted vendor, not a release", len(cmds), floor)
	}

	groups := map[string]bool{}
	for _, c := range cmds {
		if len(c.Group) > 0 {
			groups[c.Group[0]] = true
		}
	}
	for _, want := range []string{"platforms", "tenants"} {
		if !groups[want] {
			t.Errorf("command group %q is absent from the generated tree — "+
				"the vendored spec is not the issuer contract this CLI is built for", want)
		}
	}
	t.Logf("command tree: %d commands across %d top-level groups", len(cmds), len(groups))
}

// TestIssuerContractGateSelfTest proves the integrity check can actually FAIL.
// A gate whose detection is unproven is a green tick, not a verdict.
func TestIssuerContractGateSelfTest(t *testing.T) {
	meta := readIssuerContract(t)
	recorded := meta["spec_sha256"]

	sum := sha256.Sum256(openapiYAML)
	if hex.EncodeToString(sum[:]) != recorded {
		t.Fatal("precondition failed: the real spec does not match its provenance")
	}

	// One byte of tampering must change the verdict.
	tampered := append(append([]byte{}, openapiYAML...), '\n')
	tsum := sha256.Sum256(tampered)
	if hex.EncodeToString(tsum[:]) == recorded {
		t.Error("a one-byte edit to the vendored spec did NOT change its hash — " +
			"the integrity check cannot detect a hand-edit")
	}

	// An empty spec must be caught by the length guard, not silently hashed.
	if len(openapiYAML) == 0 {
		t.Error("unreachable: guarded above")
	}
	esum := sha256.Sum256(nil)
	if hex.EncodeToString(esum[:]) == recorded {
		t.Error("the recorded hash is the hash of an EMPTY file")
	}
}
