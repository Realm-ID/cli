#!/usr/bin/env python3
# VENDORED from claude-config skills/workflow-preflight @ ee0c8a770669 — edit canonical, not this copy
"""Tests for workflow_preflight.py (W1: lint + parity) — stdlib + PyYAML, no runner to install.

Fixtures are synthetic repos built in temp dirs (no checked-in tree, so the vendored copy of
this file runs anywhere). lint tests use the REAL actionlint/shellcheck on PATH, pinned to
whatever version is installed, and stub binaries for the refusal / canary cases.

  T1 lint    version refusal, SC2086 canary, SHA pins, 0-examined
  T2 parity  T2a UNMAPPED first, then every finding code, the exit-2 cases, the positive control
  T3 refs    T3a-T3j, --static, the Q4/Q7/Q8 grades; dispatch-check reads the workflow at the remote ref
             (the network is a stand-in `gh` script and a names fixture: no real network)
  T6 vendor  vendor.sh --check passes on a clean copy and fails on a one-line tamper
"""
import contextlib
import datetime
import importlib.util
import io
import json
import os
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
SHA = "a" * 40


def load():
    spec = importlib.util.spec_from_file_location("workflow_preflight", HERE / "workflow_preflight.py")
    m = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(m)
    return m


def tool_version(cmd, flag, prefix=""):
    out = subprocess.run([cmd, flag], capture_output=True, text=True).stdout
    for line in out.splitlines():
        if prefix and line.startswith(prefix):
            return line.split(":", 1)[1].strip()
    return out.splitlines()[0].strip()


CI_GOOD = f"""name: ci
on: [push, pull_request]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@{SHA}  # v4
      - run: make test
"""


class Base(unittest.TestCase):
    def setUp(self):
        self.m = load()
        self.tree = Path(tempfile.mkdtemp(prefix="workflow-preflight-"))
        self.addCleanup(shutil.rmtree, self.tree, ignore_errors=True)
        (self.tree / ".github" / "workflows").mkdir(parents=True)

    def wf(self, name, text):
        (self.tree / ".github" / "workflows" / name).write_text(text)

    def cfg(self, **kw):
        (self.tree / ".workflow-preflight.json").write_text(json.dumps(kw))

    def run_cli(self, *argv):
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            try:
                rc = self.m.main(list(argv) + ["--root", str(self.tree)])
            except SystemExit as e:
                rc = e.code
        return rc, out.getvalue() + err.getvalue()

    def stub(self, name, body):
        p = self.tree / "bin" / name
        p.parent.mkdir(exist_ok=True)
        p.write_text("#!/bin/sh\n" + body)
        p.chmod(p.stat().st_mode | stat.S_IXUSR)
        return str(p)


class LintTests(Base):
    def pins(self):
        return {"actionlint": tool_version("actionlint", "-version"),
                "shellcheck": tool_version("shellcheck", "--version", "version")}

    def test_clean(self):
        self.wf("ci.yml", CI_GOOD)
        self.cfg(tools=self.pins())
        rc, out = self.run_cli("lint")
        self.assertEqual(rc, 0, out)
        self.assertIn("examined:", out)
        self.assertIn("1 uses:", out)

    def test_wrong_actionlint_version_refused(self):
        self.wf("ci.yml", CI_GOOD)
        self.cfg(tools=self.pins())
        stub = self.stub("actionlint", 'echo 0.0.1\n')
        rc, out = self.run_cli("lint", "--actionlint", stub)
        self.assertEqual(rc, 2, out)
        self.assertIn("version mismatch", out)

    def test_wrong_shellcheck_version_refused(self):
        self.wf("ci.yml", CI_GOOD)
        self.cfg(tools=self.pins())
        stub = self.stub("shellcheck", 'echo "version: 0.0.1"\n')
        rc, out = self.run_cli("lint", "--shellcheck", stub)
        self.assertEqual(rc, 2, out)
        self.assertIn("version mismatch", out)

    def test_canary_fails_when_shellcheck_reports_nothing(self):
        self.wf("ci.yml", CI_GOOD)
        pins = self.pins()
        self.cfg(tools=pins)
        stub = self.stub("shellcheck", f'case "$1" in --version) echo "version: {pins["shellcheck"]}";; *) echo "[]";; esac\n')
        rc, out = self.run_cli("lint", "--shellcheck", stub)
        self.assertEqual(rc, 2, out)
        self.assertIn("canary", out)

    def test_unpinned_action_is_a_finding(self):
        self.wf("ci.yml", CI_GOOD.replace(SHA + "  # v4", "v4"))
        self.cfg(tools=self.pins())
        rc, out = self.run_cli("lint")
        self.assertEqual(rc, 1, out)
        self.assertIn("UNPINNED", out)

    def test_local_docker_and_reusable_uses_are_not_third_party(self):
        self.wf("ci.yml", CI_GOOD + "  call:\n    uses: o/r/.github/workflows/x.yml@v1\n"
                "  loc:\n    uses: ./.github/workflows/y.yml\n")
        self.wf("y.yml", "on: workflow_call\njobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo hi\n")
        self.cfg(tools=self.pins())
        rc, out = self.run_cli("lint")
        self.assertNotIn("UNPINNED", out)

    def test_zero_uses_lines_exit_2(self):
        self.wf("ci.yml", "on: push\njobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo hi\n")
        self.cfg(tools=self.pins())
        rc, out = self.run_cli("lint")
        self.assertEqual(rc, 2, out)
        self.assertIn("0 uses:", out)

    def test_actionlint_findings_exit_1(self):
        self.wf("ci.yml", CI_GOOD.replace("make test", "echo ${{ bogus.thing }}"))
        self.cfg(tools=self.pins())
        rc, out = self.run_cli("lint")
        self.assertEqual(rc, 1, out)
        self.assertIn("ACTIONLINT", out)

    def test_shellcheck_findings_reach_the_output(self):
        self.wf("ci.yml", CI_GOOD.replace("make test", "echo $FOO"))
        self.cfg(tools=self.pins())
        rc, out = self.run_cli("lint")
        self.assertEqual(rc, 1, out)
        self.assertIn("SC2086", out)

    def test_no_workflows_exit_2(self):
        self.cfg(tools=self.pins())
        rc, out = self.run_cli("lint")
        self.assertEqual(rc, 2, out)

    def test_lint_needs_tool_pins(self):
        self.wf("ci.yml", CI_GOOD)
        self.cfg(tools={"actionlint": self.pins()["actionlint"]})
        rc, out = self.run_cli("lint")
        self.assertEqual(rc, 2, out)
        self.assertIn("shellcheck", out)


def ci(go_version=None, extra_steps="", on="[push, pull_request]", jobs=("test",), run="make test"):
    s = f"name: ci\non: {on}\njobs:\n"
    for j in jobs:
        s += f"  {j}:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@{SHA}\n"
        if go_version:
            s += f"      - uses: actions/setup-go@{SHA}\n        with:\n          go-version: '{go_version}'\n"
        s += f"      - run: {run}\n{extra_steps}"
    return s


class ParityTests(Base):
    # ---- T2a: the first red test
    def test_t2a_unmapped_job_is_a_finding(self):
        self.wf("ci.yml", ci(jobs=("test", "integration")))
        self.cfg(jobs={"ci.yml:test": {"local": "make test"}})
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 1, out)
        self.assertIn("UNMAPPED ci.yml:integration", out)

    def test_clean_by_construction(self):
        self.wf("ci.yml", ci())
        self.cfg(jobs={"ci.yml:test": {"local": "make test"}})
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 0, out)
        self.assertIn("examined: 1 workflows, 1 in-scope jobs, 1 mapped, 0 waived", out)

    def test_waiver_without_reason_exit_2(self):
        self.wf("ci.yml", ci())
        self.cfg(jobs={"ci.yml:test": {"waiver": "ci-only"}})
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 2, out)
        self.assertIn("NO_REASON", out)

    def test_short_reason_exit_2(self):
        self.wf("ci.yml", ci())
        self.cfg(jobs={"ci.yml:test": {"waiver": "ci-only", "reason": "too short"}})
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 2, out)
        self.assertIn("NO_REASON", out)

    def test_unknown_waiver_kind_exit_2(self):
        self.wf("ci.yml", ci())
        self.cfg(jobs={"ci.yml:test": {"waiver": "whatever", "reason": "x" * 30}})
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 2, out)

    def test_waiver_counts_as_waived(self):
        self.wf("ci.yml", ci())
        self.cfg(jobs={"ci.yml:test": {"waiver": "ci-only", "reason": "needs a Windows runner here"}})
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 0, out)
        self.assertIn("0 mapped, 1 waived", out)

    def test_stale_entry(self):
        self.wf("ci.yml", ci())
        self.cfg(jobs={"ci.yml:test": {"local": "make test"}, "ci.yml:gone": {"local": "make gone"}})
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 1, out)
        self.assertIn("STALE ci.yml:gone", out)

    def test_diverged_when_ci_step_is_not_the_mapped_command(self):
        self.wf("ci.yml", ci(run="go test ./..."))
        self.cfg(jobs={"ci.yml:test": {"local": "make test"}})
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 1, out)
        self.assertIn("DIVERGED ci.yml:test", out)

    def test_install_and_run_action_is_diverged_unless_waived(self):
        extra = f"      - uses: raven-actions/actionlint@{SHA}\n"
        self.wf("ci.yml", ci(extra_steps=extra))
        self.cfg(jobs={"ci.yml:test": {"local": "make test"}})
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 1, out)
        self.assertIn("raven-actions/actionlint", out)
        self.cfg(jobs={"ci.yml:test": {"waiver": "ci-only", "reason": "uses an install-and-run action"}})
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 0, out)

    def test_pin_drift_blocks_without_adoption_date(self):
        self.wf("ci.yml", ci(go_version="1.26.0"))
        self.cfg(toolchain={"go": "1.27.2"}, jobs={"ci.yml:test": {"local": "make test"}})
        self.m.local_go_version = lambda: "1.27.2"
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 1, out)
        self.assertIn("PIN_DRIFT", out)

    def test_pin_drift_warns_inside_14_days_then_blocks(self):
        self.wf("ci.yml", ci(go_version="1.26.0"))
        self.cfg(toolchain={"go": "1.27.2"}, adopted="2026-10-10",
                 jobs={"ci.yml:test": {"local": "make test"}})
        self.m.local_go_version = lambda: "1.27.2"
        self.m.today = lambda: datetime.date(2026, 10, 20)
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 0, out)
        self.assertIn("WARN PIN_DRIFT", out)
        self.m.today = lambda: datetime.date(2026, 10, 25)  # day 15
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 1, out)
        self.assertIn("PIN_DRIFT", out)

    def test_tool_pin_drift_in_run_text(self):
        self.wf("ci.yml", ci(extra_steps="      - run: go install github.com/rhysd/actionlint/cmd/actionlint@v1.6.0\n"))
        self.cfg(tools={"actionlint": "1.7.12"}, jobs={"ci.yml:test": {"local": "make test"}})
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 1, out)
        self.assertIn("PIN_DRIFT", out)

    def test_shellcheck_version_is_not_attributed_to_actionlint(self):
        """Consumer A 2026-10-20: ci.yml: actionlint version is 0.11.0 (shellcheck's), config pins 1.7.12."""
        extra = (f"      - uses: raven-actions/actionlint@{SHA}\n        with:\n          version: 1.7.12\n"
                 "      - name: Install shellcheck\n        env:\n          SC_VERSION: v0.11.0\n"
                 "        run: |\n          echo \"actionlint shells out to shellcheck v0.11.0\"\n"
                 "          curl -sSL \"https://example.invalid/shellcheck-${SC_VERSION}.tar.xz\" | tar xJ\n")
        self.wf("ci.yml", ci(extra_steps=extra))
        self.cfg(tools={"actionlint": "1.7.12"}, jobs={"ci.yml:test": {"local": "make test"}})
        rc, out = self.run_cli("parity")
        self.assertNotIn("actionlint version is 0.11.0", out)
        self.assertNotIn("PIN_DRIFT", out)

    def _raven_job(self, raven_version):
        """Consumer A 2026-10-20 fixture, verbatim shape: job `lint` in ci.yml."""
        return ("name: ci\non: [push, pull_request]\njobs:\n  lint:\n    runs-on: ubuntu-latest\n    steps:\n"
                "      - name: install pinned shellcheck\n        env:\n          SC_VERSION: v0.11.0\n"
                "        run: echo installing\n"
                f"      - uses: raven-actions/actionlint@{SHA}\n        with:\n"
                f"          version: {raven_version}\n"
                "          flags: -shellcheck=${{ runner.temp }}/shellcheck-v0.11.0/shellcheck\n"
                # the canary step of consumer A (commit 734bf7e): actionlint and a hyphen-bound shellcheck-v0.11.0 in ONE run text
                "      - name: canary\n        shell: bash\n        run: |\n"
                "          out=$(\"$RUNNER_TEMP/actionlint-1.7.12/actionlint\" "
                "-shellcheck=\"$RUNNER_TEMP/shellcheck-v0.11.0/shellcheck\" .github/workflows/canary.yml 2>&1 || true)\n")

    def test_consumer_a_raven_flags_shellcheck_path_is_not_actionlint_version(self):
        self.wf("ci.yml", self._raven_job("1.7.12"))
        self.cfg(tools={"actionlint": "1.7.12", "shellcheck": "0.11.0"}, jobs={"ci.yml:lint": {"waiver": "ci-only", "reason": "uses an install-and-run action"}})
        rc, out = self.run_cli("parity")
        self.assertNotIn("PIN_DRIFT", out)
        self.assertEqual(rc, 0, out)

    def test_consumer_a_raven_real_actionlint_drift_still_fires(self):
        self.wf("ci.yml", self._raven_job("1.7.11"))
        self.cfg(tools={"actionlint": "1.7.12", "shellcheck": "0.11.0"}, jobs={"ci.yml:lint": {"waiver": "ci-only", "reason": "uses an install-and-run action"}})
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 1, out)
        self.assertIn("actionlint version is 1.7.11", out)
        self.assertNotIn("shellcheck version is", out)

    def test_other_tool_name_between_tool_and_version_still_pins_that_tool(self):
        self.wf("ci.yml", ci(extra_steps="      - run: echo actionlint and shellcheck v0.11.0\n"))
        self.cfg(tools={"shellcheck": "0.9.0"}, jobs={"ci.yml:test": {"local": "make test"}})
        rc, out = self.run_cli("parity")
        self.assertIn("shellcheck version is 0.11.0", out)

    def test_golangci_lint_action_version_drift(self):
        extra = f"      - uses: golangci/golangci-lint-action@{SHA}\n        with:\n          version: v1.50.0\n"
        self.wf("ci.yml", ci(extra_steps=extra))
        self.cfg(tools={"golangci-lint": "2.1.6"}, jobs={"ci.yml:test": {"local": "make test"}})
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 1, out)
        self.assertIn("PIN_DRIFT", out)

    def test_pins_that_agree_are_clean(self):
        self.wf("ci.yml", ci(go_version="1.27.2"))
        self.cfg(toolchain={"go": "1.27.2"}, jobs={"ci.yml:test": {"local": "make test"}})
        self.m.local_go_version = lambda: "1.27.2"
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 0, out)

    def test_toolchain_drift(self):
        self.wf("ci.yml", ci(go_version="1.27.2"))
        self.cfg(toolchain={"go": "1.27.2"}, jobs={"ci.yml:test": {"local": "make test"}})
        self.m.local_go_version = lambda: "1.25.0"
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 1, out)
        self.assertIn("TOOLCHAIN_DRIFT", out)

    def test_toolchain_unreadable_is_exit_2(self):
        self.wf("ci.yml", ci(go_version="1.27.2"))
        self.cfg(toolchain={"go": "1.27.2"}, jobs={"ci.yml:test": {"local": "make test"}})
        self.m.local_go_version = lambda: None
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 2, out)

    def test_zero_in_scope_jobs_with_pr_workflow_exit_2(self):
        self.wf("ci.yml", "name: ci\non: pull_request\njobs: {}\n")
        self.cfg(jobs={})
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 2, out)
        self.assertIn("0 in-scope jobs", out)

    def test_no_workflows_exit_2(self):
        self.cfg(jobs={})
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 2, out)

    def test_dispatch_only_workflow_is_out_of_scope(self):
        self.wf("ci.yml", ci())
        self.wf("sync.yml", ci(on="workflow_dispatch", jobs=("sync",)))
        self.cfg(jobs={"ci.yml:test": {"local": "make test"}})
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 0, out)
        self.assertIn("1 in-scope jobs", out)

    def test_on_mapping_form_is_in_scope(self):
        self.wf("ci.yml", ci(on="\n  pull_request:\n    paths: ['a/**']"))
        self.cfg(jobs={})
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 1, out)
        self.assertIn("UNMAPPED", out)

    def test_non_make_local_without_marker_is_no_proof(self):
        self.wf("ci.yml", ci(run="./scripts/it.sh"))
        self.cfg(jobs={"ci.yml:test": {"local": "./scripts/it.sh"}})
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 1, out)
        self.assertIn("NO_PROOF ci.yml:test", out)

    def test_marker_checked_against_gate_log(self):
        self.wf("ci.yml", ci(run="./scripts/it.sh"))
        self.cfg(jobs={"ci.yml:test": {"local": "./scripts/it.sh", "marker": "it"}})
        log = self.tree / "gate.log"
        log.write_text("running\n")
        rc, out = self.run_cli("parity", "--gate-log", str(log))
        self.assertEqual(rc, 1, out)
        self.assertIn("NO_MARKER ci.yml:test", out)
        log.write_text("running\n::preflight-step::it\n")
        rc, out = self.run_cli("parity", "--gate-log", str(log))
        self.assertEqual(rc, 0, out)

    def test_marker_without_log_is_listed_unverified(self):
        self.wf("ci.yml", ci(run="./scripts/it.sh"))
        self.cfg(jobs={"ci.yml:test": {"local": "./scripts/it.sh", "marker": "it"}})
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 0, out)
        self.assertIn("unverified", out)

    def test_markers_mode_requires_marker_even_for_make(self):
        self.wf("ci.yml", ci())
        self.cfg(parity_mode="markers", jobs={"ci.yml:test": {"local": "make test"}})
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 1, out)
        self.assertIn("NO_PROOF", out)

    def test_opt_in_slow_waiver_is_reported_as_not_run_locally(self):
        self.wf("ci.yml", ci(jobs=("test", "integration")))
        self.cfg(jobs={"ci.yml:test": {"local": "make test"},
                       "ci.yml:integration": {"waiver": "opt-in-slow", "reason": "compose suite, ten minutes"}})
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 0, out)
        self.assertIn("1 CI job(s) not run locally: ci.yml:integration", out)

    def test_paths_must_be_a_list(self):
        self.wf("ci.yml", ci())
        self.cfg(jobs={"ci.yml:test": {"local": "make test", "paths": "src/**"}})
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 2, out)

    def test_unknown_config_key_exit_2(self):
        self.wf("ci.yml", ci())
        self.cfg(jobz={})
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 2, out)

    def test_unknown_entry_key_exit_2(self):
        self.wf("ci.yml", ci())
        self.cfg(jobs={"ci.yml:test": {"local": "make test", "bogus": 1}})
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 2, out)

    def test_missing_config_exit_2(self):
        self.wf("ci.yml", ci())
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 2, out)
        self.assertIn(".workflow-preflight.json", out)

    def test_unparseable_workflow_exit_2(self):
        self.wf("ci.yml", "on: [push\n  : bad")
        self.cfg(jobs={})
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 2, out)


ORG = "Just-Git-Dev/reusable-workflows"
TAGSHA = "c" * 40


def job_yaml(jid, setup, version, extra=""):
    return (f"  {jid}:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@{SHA}\n"
            f"      - uses: actions/{setup}@{SHA}\n        with:\n          {setup.split('-')[1]}-version: '{version}'\n"
            f"      - run: make {jid}\n{extra}")


class ToolchainOverrideTests(Base):
    """Ruling 1 (2026-10-21): a per-job toolchain override with a reason."""

    def go_cli(self, **kw):
        self.wf("release.yml", "on: push\njobs:\n" + job_yaml("build", "setup-go", "1.27.2")
                + job_yaml("min-go", "setup-go", "1.23"))
        jobs = {"release.yml:build": {"local": "make build"}, "release.yml:min-go": {"local": "make min-go"}}
        self.cfg(toolchain={"go": "1.27.2"}, jobs=jobs, **kw)
        self.m.local_go_version = lambda: "1.27.2"
        return self.run_cli("parity")

    def test_without_override_the_old_job_drifts(self):
        rc, out = self.go_cli()
        self.assertEqual(rc, 1, out)
        self.assertIn("PIN_DRIFT", out)

    def test_cli_release_go_123_job_passes_with_override(self):
        rc, out = self.go_cli(toolchain_overrides={
            "release.yml:min-go": {"go": "1.23", "reason": "go.mod minimum-version build"}})
        self.assertEqual(rc, 0, out)

    def test_override_does_not_excuse_other_jobs(self):
        # the override names min-go, but `build` drifts to 1.23 too: still a finding
        self.wf("release.yml", "on: push\njobs:\n" + job_yaml("build", "setup-go", "1.23")
                + job_yaml("min-go", "setup-go", "1.23"))
        self.cfg(toolchain={"go": "1.27.2"},
                 toolchain_overrides={"release.yml:min-go": {"go": "1.23", "reason": "go.mod minimum-version build"}},
                 jobs={"release.yml:build": {"local": "make build"}, "release.yml:min-go": {"local": "make min-go"}})
        self.m.local_go_version = lambda: "1.27.2"
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 1, out)
        self.assertIn("PIN_DRIFT release.yml", out)

    def test_override_job_on_a_third_version_still_drifts(self):
        self.wf("release.yml", "on: push\njobs:\n" + job_yaml("min-go", "setup-go", "1.22"))
        self.cfg(toolchain={"go": "1.27.2"},
                 toolchain_overrides={"release.yml:min-go": {"go": "1.23", "reason": "go.mod minimum-version build"}},
                 jobs={"release.yml:min-go": {"local": "make min-go"}})
        self.m.local_go_version = lambda: "1.27.2"
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 1, out)
        self.assertIn("PIN_DRIFT", out)

    def test_sdk_node_20_ci_and_node_22_publish(self):
        self.wf("ci.yml", "on: push\njobs:\n" + job_yaml("test", "setup-node", "20")
                + job_yaml("publish", "setup-node", "22"))
        self.cfg(toolchain={"node": "22"},
                 toolchain_overrides={"ci.yml:test": {"node": "20", "reason": "sdk supports Node 20 as its minimum"}},
                 jobs={"ci.yml:test": {"local": "make test"}, "ci.yml:publish": {"local": "make publish"}})
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 0, out)
        # the publish job moving to 20 is NOT excused by the CI job's override
        self.wf("ci.yml", "on: push\njobs:\n" + job_yaml("test", "setup-node", "20")
                + job_yaml("publish", "setup-node", "20"))
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 1, out)
        self.assertIn("PIN_DRIFT", out)

    def test_missing_reason_exit_2(self):
        rc, out = self.go_cli(toolchain_overrides={"release.yml:min-go": {"go": "1.23"}})
        self.assertEqual(rc, 2, out)
        self.assertIn("NO_REASON", out)

    def test_short_reason_exit_2(self):
        rc, out = self.go_cli(toolchain_overrides={"release.yml:min-go": {"go": "1.23", "reason": "too short"}})
        self.assertEqual(rc, 2, out)
        self.assertIn("NO_REASON", out)

    def test_malformed_override_exit_2(self):
        for bad in ({"release.yml:min-go": {"reason": "r" * 25}},
                    {"release.yml:min-go": {"go": "1.23", "node": "20", "reason": "r" * 25}},
                    {"release.yml:min-go": {"rust": "1.80", "reason": "r" * 25}},
                    {"min-go": {"go": "1.23", "reason": "r" * 25}}):
            rc, out = self.go_cli(toolchain_overrides=bad)
            self.assertEqual(rc, 2, f"{bad}: {out}")

    def test_override_for_missing_job_is_stale(self):
        rc, out = self.go_cli(toolchain_overrides={
            "release.yml:min-go": {"go": "1.23", "reason": "go.mod minimum-version build"},
            "release.yml:gone": {"go": "1.23", "reason": "go.mod minimum-version build"}})
        self.assertEqual(rc, 1, out)
        self.assertIn("STALE release.yml:gone", out)

    def test_override_equal_to_toolchain_is_redundant(self):
        self.wf("release.yml", "on: push\njobs:\n" + job_yaml("build", "setup-go", "1.27.2"))
        self.cfg(toolchain={"go": "1.27.2"},
                 toolchain_overrides={"release.yml:build": {"go": "1.27.2", "reason": "pointless but long enough"}},
                 jobs={"release.yml:build": {"local": "make build"}})
        self.m.local_go_version = lambda: "1.27.2"
        rc, out = self.run_cli("parity")
        self.assertEqual(rc, 1, out)
        self.assertIn("REDUNDANT_OVERRIDE release.yml:build", out)


class OrgTagPinTests(Base):
    """Ruling 2 (2026-10-21): tag or tag-commit SHA for Just-Git-Dev/reusable-workflows."""

    def setUp(self):
        super().setUp()
        self.calls = []
        self.online = {"v3.8.1": TAGSHA}

        def fake(tag):
            self.calls.append(tag)
            return self.online.get(tag)
        self.m.remote_tag_sha = fake

    def lint(self, uses_line):
        self.wf("ci.yml", CI_GOOD + f"      - uses: {uses_line}\n")
        self.cfg(tools={"actionlint": tool_version("actionlint", "-version"),
                        "shellcheck": tool_version("shellcheck", "--version", "version")})
        return self.run_cli("lint")

    def test_exact_tag_passes_for_action_and_workflow(self):
        rc, out = self.lint(f"{ORG}/actions/deploy@v3.8.1")
        self.assertEqual(rc, 0, out)
        self.assertNotIn("UNPINNED", out)
        self.wf("ci.yml", CI_GOOD + f"  call:\n    uses: {ORG}/.github/workflows/x.yml@v3.8.1\n")
        rc, out = self.run_cli("lint")
        self.assertEqual(rc, 0, out)

    def test_sha_with_matching_tag_comment_passes_and_is_verified(self):
        rc, out = self.lint(f"{ORG}/actions/deploy@{TAGSHA}  # v3.8.1")
        self.assertEqual(rc, 0, out)
        self.assertEqual(self.calls, ["v3.8.1"])
        self.assertNotIn("NOT verified", out)

    def test_sha_without_tag_comment_is_a_finding(self):
        rc, out = self.lint(f"{ORG}/actions/deploy@{TAGSHA}")
        self.assertEqual(rc, 1, out)
        self.assertIn("NO_TAG_COMMENT", out)

    def test_floating_ref_is_unpinned(self):
        for ref in ("v3", "main", "v3.8"):
            rc, out = self.lint(f"{ORG}/actions/deploy@{ref}")
            self.assertEqual(rc, 1, f"{ref}: {out}")
            self.assertIn("UNPINNED", out)

    def test_tag_sha_mismatch_is_a_finding(self):
        rc, out = self.lint(f"{ORG}/actions/deploy@{'d' * 40}  # v3.8.1")
        self.assertEqual(rc, 1, out)
        self.assertIn("TAG_SHA_MISMATCH", out)

    def test_nonexistent_tag_is_a_mismatch(self):
        rc, out = self.lint(f"{ORG}/actions/deploy@{TAGSHA}  # v9.9.9")
        self.assertEqual(rc, 1, out)
        self.assertIn("TAG_SHA_MISMATCH", out)

    def test_offline_warns_loudly_and_does_not_fail(self):
        def down(tag):
            raise self.m.NetworkUnavailable("no route")
        self.m.remote_tag_sha = down
        rc, out = self.lint(f"{ORG}/actions/deploy@{TAGSHA}  # v3.8.1")
        self.assertEqual(rc, 0, out)
        self.assertIn("tag/SHA NOT verified (offline)", out)

    def test_offline_flag_skips_network_and_warns(self):
        self.lint(f"{ORG}/actions/deploy@{TAGSHA}  # v3.8.1")
        self.calls.clear()
        rc, out = self.run_cli("lint", "--offline")
        self.assertEqual(rc, 0, out)
        self.assertEqual(self.calls, [])
        self.assertIn("tag/SHA NOT verified (offline)", out)

    def test_other_third_party_still_needs_a_sha(self):
        rc, out = self.lint("other-org/some-action@v1.2.3")
        self.assertEqual(rc, 1, out)
        self.assertIn("UNPINNED", out)

    def test_lookalike_org_is_third_party(self):
        rc, out = self.lint("Just-Git-Dev/reusable-workflows-evil/actions/x@v3.8.1")
        self.assertEqual(rc, 1, out)
        self.assertIn("UNPINNED", out)

    def test_remote_tag_sha_parses_peeled_ls_remote(self):
        m = load()
        t, p = "b" * 40, "c" * 40
        real = m.subprocess.run

        class R:
            returncode = 0
            stdout = f"{t}\trefs/tags/v3.8.1\n{p}\trefs/tags/v3.8.1^{{}}\n"
            stderr = ""
        m.subprocess.run = lambda *a, **k: R()
        try:
            self.assertEqual(m.remote_tag_sha("v3.8.1"), p)
            R.stdout = f"{t}\trefs/tags/v3.8.1\n"  # lightweight tag
            self.assertEqual(m.remote_tag_sha("v3.8.1"), t)
            R.stdout = ""
            self.assertIsNone(m.remote_tag_sha("v3.8.1"))
            R.returncode, R.stderr = 128, "fatal: unable to access"
            with self.assertRaises(m.NetworkUnavailable):
                m.remote_tag_sha("v3.8.1")
        finally:
            m.subprocess.run = real


class CliTests(Base):
    def test_subprocess_entry_point(self):
        self.wf("ci.yml", ci(jobs=("test", "integration")))
        self.cfg(jobs={"ci.yml:test": {"local": "make test"}})
        p = subprocess.run([sys.executable, str(HERE / "workflow_preflight.py"), "parity", "--root", str(self.tree)],
                           capture_output=True, text=True)
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
        self.assertIn("examined:", p.stdout)


CALLEE = """on:
  workflow_call:
    inputs:
      env_name: {type: string, required: true}
      dry_run: {type: boolean, default: false}
      retries: {type: number, required: false}
    secrets:
      TOKEN: {required: true}
      EXTRA: {required: false}
jobs:
  j:
    runs-on: ubuntu-latest
    steps:
      - run: echo hi
"""


class CallsTests(Base):
    """W3 `calls`: the reusable-workflow contract (spec 3.4). Remote fetches are stubbed."""

    def setUp(self):
        super().setUp()
        self.resolved, self.fetched = [], []
        self.remote = {"v1.2.3": "e" * 40, "v1": "f" * 40, "main": "9" * 40}
        self.text = CALLEE
        self.fail = None

        def resolve(gh, repo, ref):
            self.resolved.append((repo, ref))
            if self.fail:
                raise self.m.CalleeUnavailable(*self.fail)
            if re.fullmatch(r"[0-9a-f]{40}", ref):
                return ref
            if ref not in self.remote:
                raise self.m.CalleeUnavailable("bad_ref", f"{ref} does not exist in {repo}")
            return self.remote[ref]

        def fetch(gh, repo, path, sha):
            self.fetched.append((repo, path, sha))
            return self.text
        self.m.resolve_ref = resolve
        self.m.fetch_callee_text = fetch
        self.m.remote_tag_sha = lambda tag: self.remote.get(tag)

    def caller(self, with_=None, secrets=None, ref="v1.2.3", uses=None):
        w = "".join(f"      {k}: {v}\n" for k, v in (with_ if with_ is not None else {"env_name": "stage"}).items())
        s = ""
        if secrets == "inherit":
            s = "    secrets: inherit\n"
        elif secrets is not None:
            s = "    secrets:\n" + "".join(f"      {k}: {v}\n" for k, v in secrets.items())
        u = uses or f"{ORG}/.github/workflows/x.yml@{ref}"
        self.wf("c.yml", f"on: workflow_dispatch\njobs:\n  call:\n    uses: {u}\n"
                         + (f"    with:\n{w}" if w else "") + s)

    def calls(self, *extra, repo="my-org/r"):
        return self.run_cli("calls", "--repo", repo, *extra)

    def test_clean_call_passes_and_prints_examined(self):
        self.caller(secrets={"TOKEN": "${{ secrets.T }}"})
        rc, out = self.calls()
        self.assertEqual(rc, 0, out)
        self.assertIn("examined 1 calls (1 remote checked, 0 local left to actionlint), 1 callee contracts", out)

    def test_unpinned_main(self):
        self.caller(ref="main", secrets={"TOKEN": "x"})
        rc, out = self.calls()
        self.assertEqual(rc, 1, out)
        self.assertIn("UNPINNED", out)

    def test_legacy_alias_v1(self):
        self.caller(ref="v1", secrets={"TOKEN": "x"})
        rc, out = self.calls()
        self.assertEqual(rc, 1, out)
        self.assertIn("LEGACY_ALIAS", out)
        self.assertNotIn("UNPINNED", out)

    def test_exact_tag_and_commented_sha_both_pass(self):
        self.caller(ref="e" * 40 + "  # v1.2.3", secrets={"TOKEN": "x"})
        rc, out = self.calls()
        self.assertEqual(rc, 0, out)
        self.assertNotIn("UNPINNED", out)

    def test_sha_whose_comment_tag_points_elsewhere(self):
        self.caller(ref="d" * 40 + "  # v1.2.3", secrets={"TOKEN": "x"})
        rc, out = self.calls()
        self.assertEqual(rc, 1, out)
        self.assertIn("TAG_SHA_MISMATCH", out)

    def test_nonexistent_tag_is_bad_ref(self):
        self.caller(ref="v9.9.9", secrets={"TOKEN": "x"})
        rc, out = self.calls()
        self.assertEqual(rc, 1, out)
        self.assertIn("BAD_REF", out)

    def test_missing_required_input(self):
        self.caller(with_={}, secrets={"TOKEN": "x"})
        rc, out = self.calls()
        self.assertEqual(rc, 1, out)
        self.assertIn("MISSING_INPUT", out)
        self.assertIn("env_name", out)

    def test_unknown_input(self):
        self.caller(with_={"env_name": "s", "bogus": "1"}, secrets={"TOKEN": "x"})
        rc, out = self.calls()
        self.assertEqual(rc, 1, out)
        self.assertIn("UNKNOWN_INPUT", out)
        self.assertIn("bogus", out)

    def test_input_type_literals(self):
        self.caller(with_={"env_name": "s", "dry_run": "maybe", "retries": "many"}, secrets={"TOKEN": "x"})
        rc, out = self.calls()
        self.assertEqual(rc, 1, out)
        self.assertEqual(out.count("INPUT_TYPE"), 2, out)
        self.caller(with_={"env_name": "s", "dry_run": "true", "retries": "3"}, secrets={"TOKEN": "x"})
        self.assertEqual(self.calls()[0], 0)
        self.caller(with_={"env_name": "true", "dry_run": "${{ inputs.d }}"}, secrets={"TOKEN": "x"})
        rc, out = self.calls()
        self.assertEqual(rc, 1, out)  # YAML bool true into a string input
        self.assertIn("INPUT_TYPE", out)

    def test_missing_secret(self):
        self.caller(secrets={})
        rc, out = self.calls()
        self.assertEqual(rc, 1, out)
        self.assertIn("MISSING_SECRET", out)
        self.assertIn("TOKEN", out)

    def test_unknown_secret(self):
        self.caller(secrets={"TOKEN": "x", "NOPE": "y"})
        rc, out = self.calls()
        self.assertEqual(rc, 1, out)
        self.assertIn("UNKNOWN_SECRET", out)

    def test_inherit_same_org_satisfies_required_secret(self):
        self.caller(secrets="inherit", uses="my-org/rw/.github/workflows/x.yml@" + "e" * 40)
        self.assertEqual(self.calls()[0], 0)

    def test_inherit_cross_org_flags_only_when_callee_requires_a_secret(self):
        self.caller(secrets="inherit")
        rc, out = self.calls()
        self.assertEqual(rc, 1, out)
        self.assertIn("INHERIT_CROSS_ORG", out)
        self.assertNotIn("MISSING_SECRET", out)
        self.text = CALLEE.replace("TOKEN: {required: true}", "TOKEN: {required: false}")
        shutil.rmtree(self.tree / ".scratch")  # the contract is cached by (repo, sha)
        rc, out = self.calls()
        self.assertEqual(rc, 0, out)

    def test_unfetchable_callee_is_cannot_verify_never_a_pass(self):
        self.caller(secrets={"TOKEN": "x"})
        self.fail = ("cannot", "HTTP 404")
        rc, out = self.calls()
        self.assertEqual(rc, 2, out)
        self.assertIn("CANNOT-VERIFY", out)

    def test_local_callee_is_left_to_actionlint(self):
        self.wf("callee.yml", CALLEE)
        self.wf("c.yml", "on: workflow_dispatch\njobs:\n  call:\n    uses: ./.github/workflows/callee.yml\n"
                         "    with:\n      nope: 1\n")
        rc, out = self.calls()
        self.assertEqual(rc, 0, out)
        for code in ("UNKNOWN_INPUT", "MISSING_INPUT", "MISSING_SECRET", "MISSING_CALLEE"):
            self.assertNotIn(code, out)
        self.assertIn("examined 1 calls (0 remote checked, 1 local left to actionlint), 0 callee contracts", out)
        self.assertEqual(self.fetched, [])

    def test_inherit_with_unknown_caller_org_is_cannot_verify(self):
        self.caller(secrets="inherit")
        self.m.repo_slug = lambda root: None
        rc, out = self.run_cli("calls")
        self.assertEqual(rc, 2, out)
        self.assertIn("CANNOT-VERIFY", out)
        self.assertIn("--repo", out)

    def test_tag_comment_belongs_to_its_own_line(self):
        u = f"{ORG}/.github/workflows/x.yml@" + "d" * 40
        self.wf("c.yml", "on: workflow_dispatch\njobs:\n"
                f"  a:\n    uses: {u}\n    with: {{env_name: s}}\n    secrets: {{TOKEN: x}}\n"
                f"  b:\n    uses: {u} # v1.2.3\n    with: {{env_name: s}}\n    secrets: {{TOKEN: x}}\n")
        rc, out = self.calls()
        self.assertEqual(rc, 1, out)
        self.assertEqual(out.count("TAG_SHA_MISMATCH"), 1, out)
        self.assertIn("TAG_SHA_MISMATCH c.yml:b", out)

    def test_yaml11_booleans_are_strings_like_github(self):
        self.caller(with_={"env_name": "no", "dry_run": "yes"}, secrets={"TOKEN": "x"})
        rc, out = self.calls()
        self.assertEqual(rc, 1, out)
        self.assertEqual(out.count("INPUT_TYPE"), 1, out)  # dry_run=yes is a string for a boolean input
        self.assertIn("with.dry_run", out)
        self.assertNotIn("with.env_name", out)

    def test_duplicate_job_ids_fail_the_self_check(self):
        u = f"{ORG}/.github/workflows/x.yml@v1.2.3"
        job = f"    uses: {u}\n    with: {{env_name: s}}\n    secrets: {{TOKEN: x}}\n"
        self.wf("c.yml", f"on: workflow_dispatch\njobs:\n  a:\n{job}  a:\n{job}")
        rc, out = self.calls()
        self.assertEqual(rc, 2, out)
        self.assertIn("2 uses: lines", out)
        self.assertIn("1 calls", out)

    def test_uses_text_inside_a_run_block_is_not_a_call(self):
        self.wf("c.yml", "on: push\njobs:\n  a:\n    runs-on: x\n    steps:\n      - run: |\n"
                         f"          uses: {ORG}/.github/workflows/x.yml@v1\n")
        rc, out = self.calls()
        self.assertEqual(rc, 0, out)

    def test_corrupt_cache_file_is_refetched_and_writes_are_atomic(self):
        self.caller(ref="e" * 40 + "  # v1.2.3", secrets={"TOKEN": "x"})
        self.assertEqual(self.calls()[0], 0)
        cdir = self.tree / ".scratch" / "workflow-preflight"
        (cf,) = list(cdir.iterdir())
        cf.write_text("on: [unclosed\n  - : :")
        self.assertEqual(self.calls()[0], 0)
        self.assertEqual(len(self.fetched), 2, self.fetched)
        self.assertIn("workflow_call", cf.read_text())
        self.assertEqual([p.name for p in cdir.iterdir()], [cf.name])  # no temp file left behind
        seen, real = [], os.replace
        self.m.os.replace = lambda a, b: (seen.append((a, b)), real(a, b))[1]
        self.addCleanup(setattr, self.m.os, "replace", real)
        cf.unlink()
        self.calls()
        self.assertEqual(len(seen), 1, seen)

    def test_cache_hit_by_repo_and_sha(self):
        self.caller(ref="e" * 40 + "  # v1.2.3", secrets={"TOKEN": "x"})
        self.calls()
        self.calls()
        self.assertEqual(len(self.fetched), 1, self.fetched)
        self.assertTrue(any((self.tree / ".scratch" / "workflow-preflight").iterdir()))

    def test_zero_calls_with_reusable_uses_lines_is_exit_2(self):
        self.caller(secrets={"TOKEN": "x"})
        self.m.reusable_calls = lambda wfs: []
        rc, out = self.calls()
        self.assertEqual(rc, 2, out)
        self.assertIn("examined 0 calls", out)

    def test_repo_without_calls_is_ok_and_says_so(self):
        self.wf("ci.yml", CI_GOOD)
        rc, out = self.calls()
        self.assertEqual(rc, 0, out)
        self.assertIn("examined 0 calls (0 remote checked, 0 local left to actionlint), 0 callee contracts", out)


# A `gh` that answers per request path from STUB_DIR/routes.json and FAILS (exit 99) on any request it
# was not told about, so a wrong path or a fetch at the wrong ref cannot pass by accident.
GH_ROUTES_STUB = r'''#!/usr/bin/env python3
import json, os, sys
d = os.environ["STUB_DIR"]
args = sys.argv[1:]
open(os.path.join(d, "requests.jsonl"), "a").write(json.dumps(args) + "\n")
path = next((a for a in args[1:] if a.startswith("repos/")), "")
r = json.load(open(os.path.join(d, "routes.json"))).get(path)
if r is None:
    sys.stderr.write("UNEXPECTED REQUEST: %s\n" % path)
    sys.exit(99)
sys.stderr.write(r.get("err", ""))
sys.stdout.write(r.get("out", ""))
sys.exit(r.get("rc", 0))
'''

SHA_E, SHA_D = "e" * 40, "d" * 40
TAG_JQ = '.object.type + " " + .object.sha'


class GhFacingCallsTests(Base):
    """`calls` through the REAL _gh_api/resolve_ref/fetch_callee_text, against a request-checking gh stub."""

    def setUp(self):
        super().setUp()
        self.m.remote_tag_sha = lambda tag: {"v1.2.3": SHA_E}.get(tag)
        self.dir = self.tree / "stub"
        self.dir.mkdir()
        gh = self.dir / "gh"
        gh.write_text(GH_ROUTES_STUB)
        gh.chmod(gh.stat().st_mode | stat.S_IXUSR)
        os.environ["STUB_DIR"] = str(self.dir)
        self.addCleanup(os.environ.pop, "STUB_DIR", None)
        self.gh = str(gh)
        self.routes = {}

    def serve(self, **routes):
        self.routes.update({k.replace("__", "/"): v for k, v in routes.items()})

    def put(self, path, out="", rc=0, err=""):
        self.routes[path] = {"out": out, "rc": rc, "err": err}

    def requests(self):
        f = self.dir / "requests.jsonl"
        return [json.loads(ln)[1:] for ln in f.read_text().splitlines()] if f.exists() else []

    def call(self, ref, third_party=False):
        u = ("other-org/rw" if third_party else ORG) + f"/.github/workflows/x.yml@{ref}"
        self.wf("c.yml", f"on: workflow_dispatch\njobs:\n  call:\n    uses: {u}\n"
                         "    with: {env_name: s}\n    secrets: {TOKEN: x}\n")
        (self.dir / "routes.json").write_text(json.dumps(self.routes))
        return self.run_cli("calls", "--repo", "my-org/r", "--gh", self.gh)

    RAW = ["-H", "Accept: application/vnd.github.raw+json"]
    WF = f"repos/{ORG}/contents/.github/workflows/x.yml"

    def test_tag_resolves_via_git_ref_tags_then_fetches_at_the_sha(self):
        self.put(f"repos/{ORG}/git/ref/tags/v1.2.3", f"commit {SHA_E}\n")
        self.put(f"{self.WF}?ref={SHA_E}", CALLEE)
        rc, out = self.call("v1.2.3")
        self.assertEqual(rc, 0, out)
        self.assertEqual(self.requests(), [
            [f"repos/{ORG}/git/ref/tags/v1.2.3", "--jq", TAG_JQ],
            self.RAW + [f"{self.WF}?ref={SHA_E}"]])

    def test_annotated_tag_is_dereferenced_to_its_commit(self):
        self.put(f"repos/{ORG}/git/ref/tags/v1.2.3", f"tag {SHA_D}\n")
        self.put(f"repos/{ORG}/git/tags/{SHA_D}", f"commit {SHA_E}\n")
        self.put(f"{self.WF}?ref={SHA_E}", CALLEE)
        rc, out = self.call("v1.2.3")
        self.assertEqual(rc, 0, out)
        self.assertEqual(self.requests()[:2], [
            [f"repos/{ORG}/git/ref/tags/v1.2.3", "--jq", TAG_JQ],
            [f"repos/{ORG}/git/tags/{SHA_D}", "--jq", TAG_JQ]])
        self.assertEqual(self.requests()[2], self.RAW + [f"{self.WF}?ref={SHA_E}"])

    def test_branch_named_like_a_tag_is_bad_ref_not_a_tag(self):
        self.put(f"repos/{ORG}/git/ref/tags/v1.2.3", rc=1, err="gh: Not Found (HTTP 404)")
        self.put(f"repos/{ORG}", ORG)  # the repo itself answers
        rc, out = self.call("v1.2.3")
        self.assertEqual(rc, 1, out)
        self.assertIn("BAD_REF", out)
        self.assertNotIn("CANNOT-VERIFY", out)
        self.assertEqual(self.requests(), [
            [f"repos/{ORG}/git/ref/tags/v1.2.3", "--jq", TAG_JQ],
            [f"repos/{ORG}", "--jq", ".full_name"]])

    def test_unreachable_repo_is_cannot_verify_not_bad_ref(self):
        self.put(f"repos/{ORG}/git/ref/tags/v1.2.3", rc=1, err="gh: Not Found (HTTP 404)")
        self.put(f"repos/{ORG}", rc=1, err="gh: Not Found (HTTP 404)")
        rc, out = self.call("v1.2.3")
        self.assertEqual(rc, 2, out)
        self.assertIn("CANNOT-VERIFY", out)
        self.assertNotIn("BAD_REF", out)

    def test_non_tag_ref_is_quoted_into_the_commits_url(self):
        self.put("repos/other-org/rw/commits/release%2F1", SHA_E + "\n")
        self.put(f"repos/other-org/rw/contents/.github/workflows/x.yml?ref={SHA_E}", CALLEE)
        rc, out = self.call("release/1", third_party=True)
        self.assertEqual(self.requests()[0], ["repos/other-org/rw/commits/release%2F1", "--jq", ".sha"])
        self.assertEqual(self.requests()[1], self.RAW + [f"repos/other-org/rw/contents/.github/workflows/x.yml?ref={SHA_E}"])

    def test_pinned_sha_is_fetched_directly_and_cached(self):
        self.put(f"{self.WF}?ref={SHA_E}", CALLEE)
        rc, out = self.call(SHA_E + "  # v1.2.3")
        self.assertEqual(rc, 0, out)
        self.assertEqual(self.requests(), [self.RAW + [f"{self.WF}?ref={SHA_E}"]])
        os.remove(self.dir / "requests.jsonl")
        rc, out = self.call(SHA_E + "  # v1.2.3")
        self.assertEqual(rc, 0, out)
        self.assertEqual(self.requests(), [])

    def test_nonexistent_sha_is_bad_ref(self):
        self.put(f"{self.WF}?ref={SHA_D}", rc=1, err="gh: No commit found for the ref (HTTP 404)")
        self.put(f"repos/{ORG}/commits/{SHA_D}", rc=1, err="gh: No commit found (HTTP 422)")
        self.put(f"repos/{ORG}", ORG)
        rc, out = self.call(SHA_D)
        self.assertEqual(rc, 1, out)
        self.assertIn("BAD_REF", out)
        self.assertNotIn("CANNOT-VERIFY", out)

    def test_fetch_failure_on_an_existing_commit_is_cannot_verify(self):
        self.put(f"{self.WF}?ref={SHA_D}", rc=1, err="gh: Not Found (HTTP 404)")
        self.put(f"repos/{ORG}/commits/{SHA_D}", SHA_D + "\n")
        rc, out = self.call(SHA_D)
        self.assertEqual(rc, 2, out)
        self.assertIn("CANNOT-VERIFY", out)

    def test_an_unexpected_request_fails_the_stub(self):
        rc, out = self.call("v1.2.3")  # no routes at all
        self.assertEqual(rc, 2, out)
        self.assertEqual(self.requests()[0][0], f"repos/{ORG}/git/ref/tags/v1.2.3")


SECRET_VALUE = "SUPER-SECRET-VALUE-8841"

# A stand-in for `gh api` (the injectable network): serves the names fixture and workflow files from
# STUB_DIR. Without --jq it prints the REAL API shape, values included, so a tool that forgets to
# project names at fetch time leaks them into the output and T3h catches it.
GH_STUB = r'''#!/usr/bin/env python3
import json, os, sys
d = os.environ["STUB_DIR"]
args = sys.argv[1:]
open(os.path.join(d, "calls.log"), "a").write(" ".join(args) + "\n")
path = next((a for a in args[1:] if a.startswith("repos/")), "")
fp = os.path.join(d, "fail.json")
for k, code in (json.load(open(fp)) if os.path.exists(fp) else {}).items():
    if k in path:
        sys.stderr.write("gh: HTTP %s\n" % code)
        sys.exit(1)
if "/contents/.github/workflows/" in path:
    wf, _, ref = path.split("/contents/.github/workflows/")[1].partition("?ref=")
    f = os.path.join(d, "contents", ref or "_default", wf)
    if not os.path.exists(f):
        sys.stderr.write("gh: Not Found (HTTP 404)\n")
        sys.exit(1)
    sys.stdout.write(open(f).read())
    sys.exit(0)
names = json.load(open(os.path.join(d, "names.json")))
tail = path.split("/", 3)[3].split("?")[0]
parts = tail.split("/")
if tail == "actions/secrets": key, items = "secrets", names["repo"]["secrets"]
elif tail == "actions/variables": key, items = "variables", names["repo"]["variables"]
elif tail == "actions/organization-secrets": key, items = "secrets", names["org"]["secrets"]
elif tail == "actions/organization-variables": key, items = "variables", names["org"]["variables"]
elif tail == "dependabot/secrets": key, items = "secrets", names.get("dependabot", [])
elif tail == "environments": key, items = "environments", list(names["environments"])
elif parts[0] == "environments" and parts[2] in ("secrets", "variables"):
    key, items = parts[2], names["environments"][parts[1]][parts[2]]
else:
    sys.stderr.write("gh: Not Found (HTTP 404)\n")
    sys.exit(1)
items = [i if isinstance(i, dict) else {"name": i, "value": "v"} for i in items]
if "--jq" in args:
    for i in items:
        print(i["name"])
else:
    print(json.dumps({"total_count": len(items), key: items}))
'''


def refs_wf(expr, environment=None, on="workflow_dispatch", extra_step=""):
    env = f"    environment: {environment}\n" if environment else ""
    return (f"on: {on}\njobs:\n  sync:\n    runs-on: ubuntu-latest\n{env}    steps:\n"
            f"      - run: echo hi\n        env:\n          K: ${{{{ {expr} }}}}\n{extra_step}")


class RefsBase(Base):
    def names(self, repo_s=(), repo_v=(), org_s=(), org_v=(), envs=None, errors=None, dependabot=None):
        n = {"repo": {"secrets": list(repo_s), "variables": list(repo_v)},
             "org": {"secrets": list(org_s), "variables": list(org_v)},
             "environments": {e: {"secrets": list(v.get("secrets", [])), "variables": list(v.get("variables", []))}
                              for e, v in (envs or {}).items()},
             "errors": errors or {}}
        if dependabot is not None:
            n["dependabot"] = dependabot
        p = self.tree / "names.json"
        p.write_text(json.dumps(n))
        return str(p)

    def refs(self, names=None, *extra, repo="o/r"):
        argv = ["refs", "--repo", repo]
        if names is not None:
            argv += ["--names-file", names]
        return self.run_cli(*argv, *extra)

    def stub_gh(self):
        d = self.tree / "stub"
        (d / "contents").mkdir(parents=True, exist_ok=True)
        gh = d / "gh"
        gh.write_text(GH_STUB)
        gh.chmod(gh.stat().st_mode | stat.S_IXUSR)
        os.environ["STUB_DIR"] = str(d)
        self.addCleanup(os.environ.pop, "STUB_DIR", None)
        return d, str(gh)


class RefsTests(RefsBase):
    def test_T3a_missing_secret_is_a_finding(self):
        self.wf("rotate.yml", refs_wf("secrets.R2_ACCESS_KEY_ID"))
        rc, out = self.refs(self.names(repo_s=["OTHER"]))
        self.assertEqual(rc, 1, out)
        self.assertIn("MISSING", out)
        self.assertIn("R2_ACCESS_KEY_ID", out)

    def test_present_in_repo_or_org_is_clean_and_prints_the_positive_control(self):
        self.wf("a.yml", refs_wf("secrets.A_KEY") + "          V: ${{ vars.B_VAR }}\n")
        rc, out = self.refs(self.names(repo_s=["A_KEY"], org_v=["B_VAR"]))
        self.assertEqual(rc, 0, out)
        self.assertRegex(out, r"examined: 1 workflows, 1 jobs, 2 references \(1 secrets, 1 vars, 0 dynamic, 0 optional, 0 GITHUB_TOKEN\)")
        self.assertIn("live names: repo 1/0, org-visible 0/1", out)

    def test_names_compare_case_insensitively_and_bracket_form_is_read(self):
        self.wf("a.yml", refs_wf("secrets['a_key']"))
        rc, out = self.refs(self.names(repo_s=["A_KEY"]))
        self.assertEqual(rc, 0, out)
        self.wf("a.yml", refs_wf("secrets['NOPE']"))
        rc, out = self.refs(self.names(repo_s=["A_KEY"]))
        self.assertEqual(rc, 1, out)
        self.assertIn("MISSING", out)

    def test_a_quoted_literal_is_not_a_reference(self):
        self.wf("a.yml", refs_wf("format('{0}', 'secrets.NOT_A_REF')") + "          R: ${{ secrets.REAL }}\n")
        rc, out = self.refs(self.names(repo_s=["REAL"]))
        self.assertEqual(rc, 0, out)
        self.assertIn("1 references", out)

    def test_T3b_env_scoped_name_in_the_wrong_environment(self):
        self.wf("d.yml", refs_wf("secrets.DB_PASSWORD", environment="prod"))
        n = self.names(envs={"stage": {"secrets": ["DB_PASSWORD"]}, "prod": {}})
        rc, out = self.refs(n)
        self.assertEqual(rc, 1, out)
        self.assertIn("ENV_MISMATCH", out)
        n = self.names(envs={"stage": {"secrets": ["DB_PASSWORD"]}, "prod": {"secrets": ["DB_PASSWORD"]}})
        rc, out = self.refs(n)
        self.assertEqual(rc, 0, out)

    def test_a_job_with_no_environment_does_not_see_an_environment_name(self):
        self.wf("d.yml", refs_wf("secrets.DB_PASSWORD"))
        rc, out = self.refs(self.names(envs={"stage": {"secrets": ["DB_PASSWORD"]}}))
        self.assertEqual(rc, 1, out)
        self.assertIn("ENV_MISMATCH", out)

    def test_T3c_calling_job_with_reads_an_env_only_var(self):
        self.wf("rel.yml", "on: workflow_dispatch\njobs:\n  call:\n    uses: o/rw/.github/workflows/rf.yml@v1.0.0\n"
                           "    with:\n      app-id: ${{ vars.RELEASE_APP_ID }}\n")
        rc, out = self.refs(self.names(envs={"release": {"variables": ["RELEASE_APP_ID"]}}))
        self.assertEqual(rc, 1, out)
        self.assertIn("ENV_ONLY_AT_CALLER", out)
        rc, out = self.refs(self.names(repo_v=["RELEASE_APP_ID"]))
        self.assertEqual(rc, 0, out)

    def test_calling_job_never_gets_the_callees_environment(self):
        # even a literal environment name elsewhere in the file does not extend a calling job's scope
        self.wf("rel.yml", "on: workflow_dispatch\njobs:\n  call:\n    uses: o/rw/.github/workflows/rf.yml@v1.0.0\n"
                           "    with:\n      x: ${{ vars.ONLY_PROD }}\n")
        rc, out = self.refs(self.names(envs={"prod": {"variables": ["ONLY_PROD"]}}))
        self.assertIn("ENV_ONLY_AT_CALLER", out)

    def test_Q7_env_secret_passed_by_name_is_unproven_not_missing(self):
        self.wf("rel.yml", "on: workflow_dispatch\njobs:\n  call:\n    uses: o/rw/.github/workflows/rf.yml@v1.0.0\n"
                           "    secrets:\n      KEY: ${{ secrets.RELEASE_APP_KEY }}\n")
        n = self.names(envs={"release": {"secrets": ["RELEASE_APP_KEY"]}})
        self.cfg(adopted=datetime.date.today().isoformat())
        rc, out = self.refs(n)
        self.assertEqual(rc, 0, out)
        self.assertIn("WARN UNPROVEN", out)
        self.assertNotIn("MISSING", out)
        # owner ruling 2026-10-21: UNPROVEN warns until proven and never escalates on a deadline
        self.cfg()  # no `adopted`
        rc, out = self.refs(n)
        self.assertEqual(rc, 0, out)
        self.assertIn("WARN UNPROVEN", out)
        self.cfg(adopted="2020-01-01")  # long past the 14-day window
        rc, out = self.refs(n)
        self.assertEqual(rc, 0, out)
        self.assertIn("WARN UNPROVEN", out)

    def test_T3d_cross_org_inherit(self):
        wf = ("on: workflow_dispatch\njobs:\n  call:\n    uses: %s/rw/.github/workflows/rf.yml@v1.0.0\n"
              "    secrets: inherit\n")
        self.wf("rel.yml", wf % "other-org")
        rc, out = self.refs(self.names(), repo="my-org/r")
        self.assertEqual(rc, 1, out)
        self.assertIn("INHERIT_CROSS_ORG", out)
        self.wf("rel.yml", wf % "my-org")
        rc, out = self.refs(self.names(), repo="my-org/r")
        self.assertEqual(rc, 0, out)

    def test_T3e_dynamic_secret_needs_a_waiver(self):
        self.wf("d.yml", refs_wf("secrets[inputs.n]"))
        n = self.names()
        rc, out = self.refs(n)
        self.assertEqual(rc, 1, out)
        self.assertIn("DYNAMIC", out)
        self.assertIn("1 dynamic", out)
        self.cfg(refs={"dynamic": {"d.yml:sync": "name comes from a workflow_dispatch choice input"}})
        rc, out = self.refs(n)
        self.assertEqual(rc, 0, out)
        self.assertIn("1 dynamic", out)
        self.cfg(adopted=datetime.date.today().isoformat())
        rc, out = self.refs(n)
        self.assertEqual((rc, "WARN DYNAMIC" in out), (0, True), out)

    def test_dynamic_environment_each_waived_value_is_checked(self):
        self.wf("d.yml", refs_wf("secrets.DB", environment="${{ inputs.target }}"))
        n = self.names(envs={"stage": {"secrets": ["DB"]}, "prod": {}})
        rc, out = self.refs(n)
        self.assertEqual(rc, 1, out)
        self.assertIn("DYNAMIC_ENV", out)
        self.cfg(refs={"dynamic_env": {"d.yml:sync": {"values": ["stage", "prod"],
                                                      "reason": "target is a choice input: stage or prod"}}})
        rc, out = self.refs(n)
        self.assertEqual(rc, 1, out)
        self.assertNotIn("DYNAMIC_ENV", out)
        self.assertRegex(out, r"ENV_MISMATCH.*prod|prod.*ENV_MISMATCH")

    def test_T3f_github_token_is_skipped_and_counted(self):
        self.wf("a.yml", refs_wf("secrets.GITHUB_TOKEN"))
        rc, out = self.refs(self.names())
        self.assertEqual(rc, 0, out)
        self.assertIn("1 GITHUB_TOKEN", out)

    def test_T3g_text_holds_secrets_but_the_parser_found_none(self):
        self.wf("a.yml", "on: push\njobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n"
                         "      - run: echo secrets.X are not expressions here\n")
        rc, out = self.refs(self.names())
        self.assertEqual(rc, 2, out)
        self.assertIn("0 references", out)

    def test_no_workflows_is_exit_2(self):
        rc, out = self.refs(self.names())
        self.assertEqual(rc, 2, out)

    def test_T3h_no_variable_value_appears_anywhere(self):
        self.wf("a.yml", refs_wf("vars.MODE") + "          S: ${{ secrets.NOPE }}\n")
        n = self.names(repo_v=[{"name": "MODE", "value": SECRET_VALUE}], org_s=[{"name": "ORG", "value": SECRET_VALUE}])
        rc, out = self.refs(n)
        self.assertEqual(rc, 1, out)
        self.assertNotIn(SECRET_VALUE, out)
        # the live path: through a gh stand-in that returns values unless the tool projects names with --jq
        d, gh = self.stub_gh()
        (d / "names.json").write_text(json.dumps({
            "repo": {"secrets": [], "variables": [{"name": "MODE", "value": SECRET_VALUE}]},
            "org": {"secrets": [], "variables": [{"name": "OV", "value": SECRET_VALUE}]},
            "environments": {"stage": {"secrets": [], "variables": [{"name": "SV", "value": SECRET_VALUE}]}}}))
        rc, out = self.refs(None, "--gh", gh)
        self.assertEqual(rc, 1, out)
        self.assertNotIn(SECRET_VALUE, out)
        calls = (d / "calls.log").read_text()
        self.assertEqual(calls.count("api"), len(calls.splitlines()), calls)
        self.assertTrue(all("--jq" in c for c in calls.splitlines()), calls)
        for f in self.tree.rglob("*"):
            if f.is_file() and f.name not in ("names.json", "gh") and ".github" not in f.parts:
                self.assertNotIn(SECRET_VALUE, f.read_text(errors="ignore"), f)

    def test_T3i_a_403_is_cannot_verify_never_clean(self):
        self.wf("a.yml", refs_wf("secrets.A_KEY"))
        rc, out = self.refs(self.names(repo_s=["A_KEY"], errors={"repo.secrets": 403}))
        self.assertEqual(rc, 2, out)
        self.assertIn("CANNOT-VERIFY", out)
        self.assertIn("403", out)
        self.assertNotIn("ok:", out)
        d, gh = self.stub_gh()
        (d / "fail.json").write_text(json.dumps({"actions/secrets": 403}))
        (d / "names.json").write_text(json.dumps({"repo": {"secrets": ["A_KEY"], "variables": []},
                                                  "org": {"secrets": [], "variables": []}, "environments": {}}))
        rc, out = self.refs(None, "--gh", gh)
        self.assertEqual(rc, 2, out)
        self.assertIn("CANNOT-VERIFY", out)

    def test_missing_gh_is_cannot_verify(self):
        self.wf("a.yml", refs_wf("secrets.A_KEY"))
        rc, out = self.refs(None, "--gh", str(self.tree / "no-such-gh"))
        self.assertEqual(rc, 2, out)
        self.assertIn("CANNOT-VERIFY", out)

    def test_T3j_emptiness_guard_makes_the_reference_optional(self):
        guard = "      - if: secrets.SLACK_URL != ''\n        run: echo notify\n        env:\n          U: ${{ secrets.SLACK_URL }}\n"
        self.wf("a.yml", refs_wf("secrets.A_KEY", extra_step=guard))
        rc, out = self.refs(self.names(repo_s=["A_KEY"]))
        self.assertEqual(rc, 0, out)
        self.assertRegex(out, r"2 optional|, 2 optional")
        self.assertNotIn("MISSING", out)

    def test_config_optional_covers_the_rest_and_needs_a_reason(self):
        self.wf("a.yml", refs_wf("secrets.SOMETIMES"))
        self.cfg(refs={"optional": {"SOMETIMES": "only set on forks that publish"}})
        rc, out = self.refs(self.names())
        self.assertEqual(rc, 0, out)
        self.assertIn("1 optional", out)
        self.cfg(refs={"optional": {"SOMETIMES": "short"}})
        rc, out = self.refs(self.names())
        self.assertEqual(rc, 2, out)
        self.cfg(refs={"surprise": {}})
        rc, out = self.refs(self.names())
        self.assertEqual(rc, 2, out)

    def test_static_makes_no_network_call_and_checks_syntax_only(self):
        self.wf("d.yml", refs_wf("secrets[inputs.n]") + "          M: ${{ secrets.ANYTHING }}\n")
        d, gh = self.stub_gh()
        rc, out = self.refs(None, "--static", "--gh", gh)
        self.assertEqual(rc, 1, out)
        self.assertIn("DYNAMIC", out)
        self.assertNotIn("MISSING", out)
        self.assertFalse((d / "calls.log").exists())
        self.wf("d.yml", refs_wf("secrets.ANYTHING"))
        rc, out = self.refs(None, "--static", "--gh", str(self.tree / "no-such-gh"))
        self.assertEqual(rc, 0, out)

    def test_callee_only_workflow_is_counted_not_resolved(self):
        self.wf("c.yml", "on:\n  workflow_call:\n    secrets:\n      TOK:\n        required: true\n"
                         "jobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo hi\n"
                         "        env:\n          T: ${{ secrets.TOK }}\n          V: ${{ vars.CALLER_VAR }}\n")
        rc, out = self.refs(self.names())
        self.assertEqual(rc, 0, out)
        self.assertIn("2 in callee-only workflows", out)

    def test_declared_workflow_call_secret_is_not_looked_up(self):
        self.wf("c.yml", "on:\n  workflow_dispatch:\n  workflow_call:\n    secrets:\n      TOK:\n        required: true\n"
                         "jobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo hi\n"
                         "        env:\n          T: ${{ secrets.TOK }}\n")
        rc, out = self.refs(self.names())
        self.assertEqual(rc, 0, out)

    def test_Q8_pull_request_secret_absent_from_dependabot_store_warns_only(self):
        self.wf("pr.yml", refs_wf("secrets.A_KEY", on="pull_request"))
        rc, out = self.refs(self.names(repo_s=["A_KEY"], dependabot=[]))
        self.assertEqual(rc, 0, out)
        self.assertIn("WARN DEPENDABOT_GAP", out)
        rc, out = self.refs(self.names(repo_s=["A_KEY"], dependabot=["A_KEY"]))
        self.assertNotIn("DEPENDABOT_GAP", out)


DISPATCH_REMOTE = refs_wf("secrets.R2_ACCESS_KEY_ID").replace(
    "on: workflow_dispatch",
    "on:\n  workflow_dispatch:\n    inputs:\n      mode:\n        type: choice\n        options: [dry, apply]\n"
    "        required: true\n      note:\n        type: string\n")
DISPATCH_LOCAL = refs_wf("secrets.PRESENT")


class DispatchCheckTests(RefsBase):
    def setup_remote(self, remote=DISPATCH_REMOTE, secrets=("PRESENT",)):
        d, gh = self.stub_gh()
        (d / "contents" / "main").mkdir(parents=True, exist_ok=True)
        (d / "contents" / "main" / "x.yml").write_text(remote)
        (d / "names.json").write_text(json.dumps({"repo": {"secrets": list(secrets), "variables": []},
                                                  "org": {"secrets": [], "variables": []}, "environments": {}}))
        self.wf("x.yml", DISPATCH_LOCAL)  # the checked-out file differs from the remote one
        return d, gh

    def dc(self, gh, *extra):
        return self.run_cli("dispatch-check", "-R", "o/r", "x.yml", "--ref", "main", "--gh", gh, *extra)

    def test_the_remote_workflow_decides_not_the_local_file(self):
        d, gh = self.setup_remote()
        rc, out = self.dc(gh, "-f", "mode=dry")
        self.assertEqual(rc, 1, out)
        self.assertIn("MISSING", out)
        self.assertIn("R2_ACCESS_KEY_ID", out)
        self.assertIn("contents/.github/workflows/x.yml?ref=main", (d / "calls.log").read_text())
        self.assertIn("secrets-named: 1", out)

    def test_passes_when_the_remote_names_resolve(self):
        d, gh = self.setup_remote(secrets=("R2_ACCESS_KEY_ID",))
        rc, out = self.dc(gh, "-f", "mode=dry")
        self.assertEqual(rc, 0, out)

    def test_input_checks(self):
        d, gh = self.setup_remote(secrets=("R2_ACCESS_KEY_ID",))
        rc, out = self.dc(gh, "-f", "mode=dry", "-f", "bogus=1")
        self.assertEqual((rc, "UNKNOWN_DISPATCH_INPUT" in out), (1, True), out)
        rc, out = self.dc(gh)
        self.assertEqual((rc, "MISSING_DISPATCH_INPUT" in out), (1, True), out)
        rc, out = self.dc(gh, "-f", "mode=explode")
        self.assertEqual((rc, "BAD_CHOICE" in out), (1, True), out)

    def test_workflow_without_dispatch_trigger(self):
        d, gh = self.setup_remote(remote=refs_wf("secrets.PRESENT", on="push"))
        rc, out = self.dc(gh)
        self.assertEqual((rc, "NOT_DISPATCHABLE" in out), (1, True), out)

    def test_unreadable_remote_workflow_is_cannot_verify(self):
        d, gh = self.setup_remote()
        rc, out = self.run_cli("dispatch-check", "-R", "o/r", "x.yml", "--ref", "nope", "--gh", gh)
        self.assertEqual(rc, 2, out)
        self.assertIn("CANNOT-VERIFY", out)

    def test_cannot_verify_names_still_reports_how_many_secrets_are_named(self):
        d, gh = self.setup_remote()
        (d / "fail.json").write_text(json.dumps({"actions/secrets": 403}))
        rc, out = self.dc(gh, "-f", "mode=dry")
        self.assertEqual(rc, 2, out)
        self.assertIn("secrets-named: 1", out)

    def test_another_repos_config_is_not_used(self):
        d, gh = self.setup_remote()
        self.cfg(refs={"optional": {"R2_ACCESS_KEY_ID": "this repo's waiver, not the target's"}})
        rc, out = self.dc(gh, "-f", "mode=dry")
        self.assertEqual(rc, 1, out)
        rc, out = self.dc(gh, "-f", "mode=dry", "--config", str(self.tree / ".workflow-preflight.json"))
        self.assertEqual(rc, 0, out)


@unittest.skipUnless((HERE / "vendor.sh").is_file(),
                     "SKIPPED: vendor.sh is not beside this test (a vendored copy); "
                     "the vendor tests run only from the canonical skill checkout")
class VendorTests(unittest.TestCase):
    """T6: vendor.sh must exist next to the script. In a vendored copy it does not, so the class
    is skipped with the reason above (reported by unittest, never silently missing)."""

    def setUp(self):
        self.repo = Path(tempfile.mkdtemp(prefix="workflow-preflight-vendor-"))
        self.addCleanup(shutil.rmtree, self.repo, ignore_errors=True)

    def vendor(self, *args):
        return subprocess.run(["bash", str(HERE / "vendor.sh"), str(self.repo), *args], capture_output=True, text=True)

    def test_check_passes_on_clean_copy_and_fails_on_tamper(self):
        p = self.vendor()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        p = self.vendor("--check")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("compared 2 file(s), 0 drifted", p.stdout)
        v = self.repo / "scripts" / "workflow_preflight.py"
        v.write_text(v.read_text() + "# tamper\n")
        p = self.vendor("--check")
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
        self.assertIn("DRIFT", p.stdout)

    def test_check_on_absent_copy_exit_2(self):
        p = self.vendor("--check")
        self.assertEqual(p.returncode, 2, p.stdout + p.stderr)

    def test_deleted_stamp_reads_as_drift(self):
        self.vendor()
        v = self.repo / "scripts" / "workflow_preflight.py"
        lines = v.read_text().splitlines(keepends=True)
        del lines[1]
        v.write_text("".join(lines))
        p = self.vendor("--check")
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)


if __name__ == "__main__":
    unittest.main()
