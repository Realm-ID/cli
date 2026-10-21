#!/usr/bin/env python3
# VENDORED from claude-config skills/workflow-preflight @ 6fe86373a0ed — edit canonical, not this copy
"""Tests for workflow_preflight.py (W1: lint + parity) — stdlib + PyYAML, no runner to install.

Fixtures are synthetic repos built in temp dirs (no checked-in tree, so the vendored copy of
this file runs anywhere). lint tests use the REAL actionlint/shellcheck on PATH, pinned to
whatever version is installed, and stub binaries for the refusal / canary cases.

  T1 lint    version refusal, SC2086 canary, SHA pins, 0-examined
  T2 parity  T2a UNMAPPED first, then every finding code, the exit-2 cases, the positive control
  T6 vendor  vendor.sh --check passes on a clean copy and fails on a one-line tamper
"""
import contextlib
import datetime
import importlib.util
import io
import json
import os
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
        """AutoMahn 2026-10-20: ci.yml: actionlint version is 0.11.0 (shellcheck's), config pins 1.7.12."""
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
        """AutoMahn 2026-10-20 fixture, verbatim shape: job `lint` in ci.yml."""
        return ("name: ci\non: [push, pull_request]\njobs:\n  lint:\n    runs-on: ubuntu-latest\n    steps:\n"
                "      - name: install pinned shellcheck\n        env:\n          SC_VERSION: v0.11.0\n"
                "        run: echo installing\n"
                f"      - uses: raven-actions/actionlint@{SHA}\n        with:\n"
                f"          version: {raven_version}\n"
                "          flags: -shellcheck=${{ runner.temp }}/shellcheck-v0.11.0/shellcheck\n"
                # the canary step of automahn 734bf7e: actionlint and a hyphen-bound shellcheck-v0.11.0 in ONE run text
                "      - name: canary\n        shell: bash\n        run: |\n"
                "          out=$(\"$RUNNER_TEMP/actionlint-1.7.12/actionlint\" "
                "-shellcheck=\"$RUNNER_TEMP/shellcheck-v0.11.0/shellcheck\" .github/workflows/canary.yml 2>&1 || true)\n")

    def test_automahn_raven_flags_shellcheck_path_is_not_actionlint_version(self):
        self.wf("ci.yml", self._raven_job("1.7.12"))
        self.cfg(tools={"actionlint": "1.7.12", "shellcheck": "0.11.0"}, jobs={"ci.yml:lint": {"waiver": "ci-only", "reason": "uses an install-and-run action"}})
        rc, out = self.run_cli("parity")
        self.assertNotIn("PIN_DRIFT", out)
        self.assertEqual(rc, 0, out)

    def test_automahn_raven_real_actionlint_drift_still_fires(self):
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
    def test_unbuilt_subcommands_exit_2(self):
        for sub in ("refs", "calls", "dispatch-check"):
            rc, out = self.run_cli(sub)
            self.assertEqual(rc, 2, out)
            self.assertIn("not built yet (W2)", out)

    def test_subprocess_entry_point(self):
        self.wf("ci.yml", ci(jobs=("test", "integration")))
        self.cfg(jobs={"ci.yml:test": {"local": "make test"}})
        p = subprocess.run([sys.executable, str(HERE / "workflow_preflight.py"), "parity", "--root", str(self.tree)],
                           capture_output=True, text=True)
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
        self.assertIn("examined:", p.stdout)


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
