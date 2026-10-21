#!/usr/bin/env python3
# VENDORED from claude-config skills/workflow-preflight @ 6a157daeda00 — edit canonical, not this copy
"""workflow-preflight: stop GitHub Actions failures before push (W1: `lint` and `parity`).

Canonical copy lives in claude-config `skills/workflow-preflight/scripts/`; repos carry a stamped
vendored copy (vendor.sh). Never edit a vendored copy. See SKILL.md for the codes and config.

  workflow_preflight.py lint   [--root R] [--config F] [--actionlint P] [--shellcheck P]
  workflow_preflight.py parity [--root R] [--config F] [--gate-log FILE]
  refs / calls / dispatch-check  -> exit 2 "not built yet (W2)"

Exit 0 clean (WARN lines allowed), 1 findings, 2 usage / config / parse error or a check that
could not be verified (wrong tool version, canary silent, 0 items examined). Every subcommand
prints an `examined:` line.

Known limits (by design, W1): the tool-version scan is a regex over step text (`run`, `env`,
`with`), so a version mentioned next to a tool name in prose is read as a pin; parity cannot see
what the local gate really runs, which is why a non-`make` entry needs a runtime marker; needs
PyYAML (stdlib has no YAML parser) and exits 2 without it.
"""
import argparse
import datetime
import json
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

try:
    import yaml
except ImportError:  # pragma: no cover - exercised only where PyYAML is absent
    yaml = None

CONFIG_NAME = ".workflow-preflight.json"
TOP_KEYS = {"tools", "toolchain", "toolchain_overrides", "local_gate", "parity_mode", "jobs", "refs", "network", "adopted"}
ENTRY_KEYS_LOCAL = {"local", "marker", "paths"}
ENTRY_KEYS_WAIVER = {"waiver", "reason"}
WAIVER_KINDS = {"ci-only", "opt-in-slow", "release-only"}
MIN_REASON = 20
PARITY_TRIGGERS = {"push", "pull_request", "pull_request_target", "merge_group"}
INSTALL_AND_RUN = ("raven-actions/actionlint",)
SETUP_ACTIONS = {"actions/setup-go": ("go", "go-version"),
                 "actions/setup-node": ("node", "node-version"),
                 "actions/setup-python": ("python", "python-version")}
TOOLCHAIN_LANGS = ("go", "node", "python")
GRACE_DAYS = 14
ORG_REPO = "just-git-dev/reusable-workflows"  # compared lowercase; GitHub names are case-insensitive
ORG_URL = "https://github.com/Just-Git-Dev/reusable-workflows"
TAG_RE = re.compile(r"v\d+\.\d+\.\d+")
TAG_COMMENT_RE = re.compile(r"#\s*(v\d+\.\d+\.\d+)(?![\w.])")
CANARY = "on: push\njobs:\n  c:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo $FOO\n"
USES_RE = re.compile(r"^\s*(?:-\s*)?uses:\s*['\"]?([^\s'\"#]+)")
KNOWN_TOOLS = {"actionlint", "shellcheck", "golangci-lint"}  # tools whose names can sit beside another's version
VERSION_RE =re.compile(r"v?(\d+\.\d+\.\d+)")
MARKER_PREFIX = "::preflight-step::"


class Fatal(Exception):
    """Usage / config / parse / cannot-verify: exit 2."""


class NetworkUnavailable(Exception):
    """The tag lookup could not reach the remote: lint warns loudly, never fails or passes silently."""


def remote_tag_sha(tag):
    """Commit SHA that `tag` of the org repo peels to, or None when the tag does not exist.
    Raises NetworkUnavailable when ls-remote cannot answer. Tests replace this function."""
    try:
        p = subprocess.run(["git", "ls-remote", ORG_URL, f"refs/tags/{tag}", f"refs/tags/{tag}^{{}}"],
                           capture_output=True, text=True, timeout=30)
    except (OSError, subprocess.SubprocessError) as e:
        raise NetworkUnavailable(str(e))
    if p.returncode != 0:
        raise NetworkUnavailable((p.stderr.strip().splitlines() or [f"rc={p.returncode}"])[0])
    shas = {}
    for ln in p.stdout.splitlines():
        sha, _, ref = ln.partition("\t")
        shas[ref.strip()] = sha.strip()
    return shas.get(f"refs/tags/{tag}^{{}}") or shas.get(f"refs/tags/{tag}")


def today():
    return datetime.date.today()


def local_go_version():
    """`go env GOVERSION` without the `go` prefix, or None when it cannot be read."""
    try:
        p = subprocess.run(["go", "env", "GOVERSION"], capture_output=True, text=True, timeout=30)
    except (OSError, subprocess.SubprocessError):
        return None
    v = p.stdout.strip()
    return v[2:] if p.returncode == 0 and v.startswith("go") else None


def norm(v):
    v = str(v).strip().strip("'\"")
    return v[1:] if v[:1] == "v" else v


# ---------------------------------------------------------------- config

def load_config(path):
    if not path.is_file():
        raise Fatal(f"{path} is absent; {CONFIG_NAME} is required (see SKILL.md)")
    try:
        cfg = json.loads(path.read_text())
    except ValueError as e:
        raise Fatal(f"{path}: invalid JSON: {e}")
    if not isinstance(cfg, dict):
        raise Fatal(f"{path}: top level must be an object")
    unknown = sorted(set(cfg) - TOP_KEYS)
    if unknown:
        raise Fatal(f"{path}: unknown key(s) {unknown}; allowed {sorted(TOP_KEYS)}")
    for k in ("tools", "toolchain", "toolchain_overrides", "jobs"):
        if k in cfg and not isinstance(cfg[k], dict):
            raise Fatal(f"{path}: '{k}' must be an object")
    if cfg.get("parity_mode", "by-construction") not in ("by-construction", "markers"):
        raise Fatal(f"{path}: parity_mode must be 'by-construction' or 'markers'")
    if "adopted" in cfg:
        try:
            datetime.date.fromisoformat(str(cfg["adopted"]))
        except ValueError:
            raise Fatal(f"{path}: adopted must be YYYY-MM-DD")
    return cfg


def validate_jobs(jobs):
    """Return (errors, no_reason). no_reason lines are NO_REASON findings (exit 2)."""
    errors, no_reason = [], []
    for key, e in jobs.items():
        if not isinstance(e, dict):
            errors.append(f"jobs['{key}'] must be an object")
            continue
        has_l, has_w = "local" in e, "waiver" in e
        if has_l == has_w:
            errors.append(f"jobs['{key}'] needs exactly one of 'local' or 'waiver'")
            continue
        allowed = ENTRY_KEYS_LOCAL if has_l else ENTRY_KEYS_WAIVER
        bad = sorted(set(e) - allowed)
        if bad:
            errors.append(f"jobs['{key}'] unknown key(s) {bad}; allowed {sorted(allowed)}")
            continue
        if has_l:
            if not isinstance(e["local"], str) or not e["local"].strip():
                errors.append(f"jobs['{key}'].local must be a non-empty string")
            if "marker" in e and (not isinstance(e["marker"], str) or not e["marker"]):
                errors.append(f"jobs['{key}'].marker must be a non-empty string")
            if "paths" in e and not (isinstance(e["paths"], list) and e["paths"]
                                     and all(isinstance(p, str) for p in e["paths"])):
                errors.append(f"jobs['{key}'].paths must be a non-empty list of globs")
        else:
            if e["waiver"] not in WAIVER_KINDS:
                errors.append(f"jobs['{key}'].waiver must be one of {sorted(WAIVER_KINDS)}")
            elif len(str(e.get("reason", "")).strip()) < MIN_REASON:
                no_reason.append(f"NO_REASON {key}: a waiver needs a reason of at least {MIN_REASON} characters")
    return errors, no_reason


def validate_overrides(ov):
    """Return (errors, no_reason) for toolchain_overrides: {"<wf>:<job>": {"go"|"node"|"python": v, "reason": r}}."""
    errors, no_reason = [], []
    for key, e in ov.items():
        if not re.fullmatch(r"[^:\s]+:[^:\s]+", key):
            errors.append(f"toolchain_overrides['{key}'] must be keyed '<workflow file>:<job id>'")
            continue
        if not isinstance(e, dict):
            errors.append(f"toolchain_overrides['{key}'] must be an object")
            continue
        langs = sorted(set(e) & set(TOOLCHAIN_LANGS))
        bad = sorted(set(e) - set(TOOLCHAIN_LANGS) - {"reason"})
        if bad or len(langs) != 1:
            errors.append(f"toolchain_overrides['{key}'] needs exactly one of {list(TOOLCHAIN_LANGS)} plus 'reason'"
                          + (f"; unknown key(s) {bad}" if bad else ""))
            continue
        if not str(e[langs[0]]).strip():
            errors.append(f"toolchain_overrides['{key}'].{langs[0]} must be a non-empty version")
        elif len(str(e.get("reason", "")).strip()) < MIN_REASON:
            no_reason.append(f"NO_REASON {key}: a toolchain override needs a reason of at least {MIN_REASON} characters")
    return errors, no_reason


# ---------------------------------------------------------------- workflows

def workflow_files(root):
    d = root / ".github" / "workflows"
    return sorted(list(d.glob("*.yml")) + list(d.glob("*.yaml"))) if d.is_dir() else []


def load_workflows(root):
    if yaml is None:
        raise Fatal("PyYAML is required (pip install pyyaml); refusing to guess without a YAML parser")
    out = []
    for f in workflow_files(root):
        try:
            doc = yaml.safe_load(f.read_text())
        except yaml.YAMLError as e:
            raise Fatal(f"{f}: cannot parse: {str(e).splitlines()[0]}")
        if not isinstance(doc, dict):
            raise Fatal(f"{f}: not a workflow mapping")
        on = doc.get("on", doc.get(True))  # PyYAML reads the bare key `on` as True
        if isinstance(on, str):
            triggers = {on}
        elif isinstance(on, list):
            triggers = {str(t) for t in on}
        elif isinstance(on, dict):
            triggers = {str(t) for t in on}
        else:
            triggers = set()
        jobs = doc.get("jobs") or {}
        if not isinstance(jobs, dict):
            raise Fatal(f"{f}: 'jobs' is not a mapping")
        out.append({"name": f.name, "path": f, "doc": doc, "triggers": triggers, "jobs": jobs})
    return out


def steps_of(job):
    s = job.get("steps") if isinstance(job, dict) else None
    return [x for x in s if isinstance(x, dict)] if isinstance(s, list) else []


def run_lines(job):
    lines = []
    for st in steps_of(job):
        if isinstance(st.get("run"), str):
            lines += [ln.strip() for ln in st["run"].splitlines() if ln.strip()]
    return lines


def step_strings(step):
    """Every string a step could carry a tool version in: run text, env values, with values."""
    vals = []
    if isinstance(step.get("run"), str):
        vals.append(step["run"])
    for k in ("env", "with"):
        if isinstance(step.get(k), dict):
            vals += [str(v) for v in step[k].values()]
    return vals


# ---------------------------------------------------------------- lint

def tool_path(explicit, name):
    p = explicit or shutil.which(name)
    if not p:
        raise Fatal(f"{name} not found on PATH (pin it in tools and install it, or pass --{name} PATH)")
    return p


def first_version(cmd, flag, prefix):
    try:
        p = subprocess.run([cmd, flag], capture_output=True, text=True, timeout=30)
    except (OSError, subprocess.SubprocessError) as e:
        raise Fatal(f"cannot run {cmd} {flag}: {e}")
    for line in (p.stdout + p.stderr).splitlines():
        if prefix is None:
            return line.strip()
        if line.startswith(prefix):
            return line.split(":", 1)[1].strip()
    return ""


def cmd_lint(root, cfg, args):
    tools = cfg.get("tools", {})
    for t in ("actionlint", "shellcheck"):
        if not tools.get(t):
            raise Fatal(f"lint needs tools.{t} in {CONFIG_NAME} (the pinned version)")
    al = tool_path(args.actionlint, "actionlint")
    sc = str(Path(tool_path(args.shellcheck, "shellcheck")).resolve())
    for name, path, flag, prefix in (("actionlint", al, "-version", None), ("shellcheck", sc, "--version", "version")):
        have = first_version(path, flag, prefix)
        if norm(have) != norm(tools[name]):
            raise Fatal(f"version mismatch: {name} ({path}) reports {have or 'nothing'}, config pins {tools[name]}")

    # Canary: a wrong shellcheck path prints `Rule "shellcheck" was disabled` and exits 0.
    with tempfile.TemporaryDirectory(prefix="workflow-preflight-canary-") as d:
        cf = Path(d) / "canary.yml"
        cf.write_text(CANARY)
        p = subprocess.run([al, "-no-color", f"-shellcheck={sc}", str(cf)], capture_output=True, text=True)
    if "SC2086" not in p.stdout + p.stderr:
        raise Fatal("canary failed: actionlint did not report SC2086 for `echo $FOO`; shellcheck did not really run")

    files = workflow_files(root)
    findings = []
    if files:
        p = subprocess.run([al, "-no-color", f"-shellcheck={sc}"] + [str(f.relative_to(root)) for f in files],
                           capture_output=True, text=True, cwd=root)
        if p.returncode not in (0, 1):
            raise Fatal(f"actionlint failed to run (rc={p.returncode}): {(p.stderr or p.stdout).strip()[:300]}")
        findings += [f"ACTIONLINT {ln}" for ln in p.stdout.splitlines() if re.match(r"^\S+:\d+:\d+: ", ln)]

    uses = checked = 0
    to_verify = []
    for f in files:
        for n, line in enumerate(f.read_text().splitlines(), 1):
            m = USES_RE.match(line)
            if not m or line.lstrip().startswith("#"):
                continue
            uses += 1
            target = m.group(1)
            path, _, ref = target.partition("@")
            where = f"{f.relative_to(root)}:{n}"
            if (path.lower() + "/").startswith(ORG_REPO + "/"):
                checked += 1
                if TAG_RE.fullmatch(ref):
                    continue
                if not re.fullmatch(r"[0-9a-f]{40}", ref):
                    findings.append(f"UNPINNED {where}: {target} must be an exact tag vX.Y.Z or a 40-hex SHA with a # vX.Y.Z comment")
                    continue
                c = TAG_COMMENT_RE.search(line)
                if not c:
                    findings.append(f"NO_TAG_COMMENT {where}: {target} is a SHA pin without a `# vX.Y.Z` comment naming its tag")
                else:
                    to_verify.append((where, target, ref, c.group(1)))
                continue
            if path.startswith(("./", "docker://")) or "/.github/workflows/" in path:
                continue
            checked += 1
            if not re.fullmatch(r"[0-9a-f]{40}", ref):
                findings.append(f"UNPINNED {f.relative_to(root)}:{n}: {target} is not pinned to a full commit SHA")
    notes, resolved, offline_why = [], {}, None
    for where, target, sha, tag in to_verify:
        if tag not in resolved and offline_why is None and not args.offline:
            try:
                resolved[tag] = remote_tag_sha(tag)
            except NetworkUnavailable as e:
                offline_why = str(e)
        if args.offline or offline_why is not None:
            continue
        if resolved[tag] != sha:
            findings.append(f"TAG_SHA_MISMATCH {where}: {tag} "
                            + (f"points at {resolved[tag]}" if resolved[tag] else "does not exist")
                            + f", but the pin is {sha}")
    if to_verify and (args.offline or offline_why is not None):
        print(f"WARNING: tag/SHA NOT verified (offline): {len(to_verify)} SHA pin(s) into {ORG_REPO} "
              f"were not checked against their # vX.Y.Z tag"
              + ("" if args.offline else f" ({offline_why})"))
    elif to_verify:
        notes.append(f"{len(to_verify)} tag/SHA pin(s) into {ORG_REPO} verified against the remote")
    summary = f"examined: {len(files)} workflows, {uses} uses: lines ({checked} third-party checked)"
    return finish(findings, [], summary, notes=notes, examined=len(files) and uses)


# ---------------------------------------------------------------- parity

def pin_findings(wfs, cfg):
    """(key, message) for every tool / toolchain version in a workflow that differs from config."""
    out, seen = [], set()
    tools, chain = cfg.get("tools", {}), cfg.get("toolchain", {})
    overrides = cfg.get("toolchain_overrides", {})

    def drift(wf, what, have, want):
        if norm(have) != norm(want) and (wf, what, have) not in seen:
            seen.add((wf, what, have))
            out.append(f"{wf}: {what} is {have}, config pins {want}")

    for w in wfs:
        for jid, job in w["jobs"].items():
            ov = overrides.get(f"{w['name']}:{jid}") or {}  # per-job override: this job only
            for st in steps_of(job):
                use = str(st.get("uses", "")).split("@")[0]
                if use in SETUP_ACTIONS:
                    key, field = SETUP_ACTIONS[use]
                    v = (st.get("with") or {}).get(field)
                    want = ov.get(key, chain.get(key))
                    if want is not None and v is not None and "${{" not in str(v):
                        drift(f"{w['name']}:{jid}" if key in ov else w["name"], f"{field}", v, want)
                if use == "golangci/golangci-lint-action" and "golangci-lint" in tools:
                    v = (st.get("with") or {}).get("version")
                    if v is not None and "${{" not in str(v):
                        drift(w["name"], "golangci-lint version", v, tools["golangci-lint"])
                # an action named for the tool (raven-actions/actionlint): its own `version` input
                tool_action = use.rsplit("/", 1)[-1]
                if tool_action in tools and tool_action != "golangci-lint-action":
                    v = (st.get("with") or {}).get("version")
                    if v is not None and "${{" not in str(v) and VERSION_RE.search(str(v)):
                        drift(w["name"], f"{tool_action} version", VERSION_RE.search(str(v)).group(1), tools[tool_action])
                for text in step_strings(st):
                    for tool, want in tools.items():
                        # boundaries are alphanumeric only: `shellcheck-v0.11.0` and `-shellcheck=` ARE names
                        pat = rf"(?<![A-Za-z0-9]){re.escape(tool)}(?![A-Za-z0-9])[^\n]{{0,60}}?{VERSION_RE.pattern}"
                        for m in re.finditer(pat, text):
                            # the version belongs to the nearest tool: another tool's name between
                            # this one and the version means it is that tool's (shellcheck vX near actionlint)
                            between = text[m.start():m.start(1)][len(tool):]
                            if any(re.search(rf"(?<![A-Za-z0-9]){re.escape(o)}(?![A-Za-z0-9])", between)
                                   for o in (set(tools) | KNOWN_TOOLS) - {tool}):
                                continue
                            drift(w["name"], f"{tool} version", m.group(1), want)
    return out


def cmd_parity(root, cfg, args):
    jobs_cfg = cfg.get("jobs", {})
    errors, no_reason = validate_jobs(jobs_cfg)
    overrides = cfg.get("toolchain_overrides", {})
    e2, n2 = validate_overrides(overrides)
    errors, no_reason = errors + e2, no_reason + n2
    if no_reason:
        print("\n".join(no_reason))
        raise Fatal(f"{len(no_reason)} waiver(s) without a reason")
    if errors:
        raise Fatal("; ".join(errors))
    wfs = load_workflows(root)
    scoped = [(w, jid, job) for w in wfs if w["triggers"] & PARITY_TRIGGERS for jid, job in w["jobs"].items()]
    keys = {f"{w['name']}:{jid}" for w, jid, _ in scoped}
    mapped = sum(1 for k in keys if "local" in jobs_cfg.get(k, {}))
    waived = sum(1 for k in keys if "waiver" in jobs_cfg.get(k, {}))
    summary = (f"examined: {len(wfs)} workflows, {len(scoped)} in-scope jobs, "
               f"{mapped} mapped, {waived} waived")
    if not wfs or not scoped:
        print(summary)
        trig = [w["name"] for w in wfs if w["triggers"] & PARITY_TRIGGERS]
        raise Fatal("0 in-scope jobs" + (f" although {trig} has a push/pull_request trigger" if trig else
                                         " (no workflow with a push/pull_request/merge_group trigger)"))

    findings, warns = [], []
    mode = cfg.get("parity_mode", "by-construction")
    log = None
    if args.gate_log:
        gl = Path(args.gate_log)
        if not gl.is_file():
            raise Fatal(f"--gate-log {gl} does not exist")
        log = gl.read_text()
    unverified, not_local = [], []

    for w, jid, job in scoped:
        key = f"{w['name']}:{jid}"
        e = jobs_cfg.get(key)
        if e is None:
            findings.append(f"UNMAPPED {key}: no entry in {CONFIG_NAME} jobs (map it to a local step or waive it)")
            continue
        if "waiver" in e:
            if e["waiver"] == "opt-in-slow":
                not_local.append(key)
            continue
        cmd, marker = e["local"].strip(), e.get("marker")
        for st in steps_of(job):
            for use in INSTALL_AND_RUN:
                if str(st.get("uses", "")).split("@")[0] == use:
                    findings.append(f"DIVERGED {key}: step uses {use}, which installs and runs the tool in one "
                                    "step; the laptop cannot run the same thing (waive the job or call the local command)")
        if mode == "by-construction" and cmd.startswith("make "):
            if cmd not in run_lines(job):
                findings.append(f"DIVERGED {key}: no run step is exactly `{cmd}`")
        elif not marker:
            findings.append(f"NO_PROOF {key}: `{cmd}` is not a CI `make` step; add a marker the local gate prints "
                            f"({MARKER_PREFIX}<name>), a map entry alone proves nothing")
        if marker:
            if log is None:
                unverified.append(f"{key}={marker}")
            elif MARKER_PREFIX + marker not in log:
                findings.append(f"NO_MARKER {key}: gate log has no `{MARKER_PREFIX}{marker}` line, the step did not run")

    for k in sorted(set(jobs_cfg) - keys):
        findings.append(f"STALE {k}: no such in-scope job (deleted, renamed, or its workflow is not push/PR-triggered)")

    all_jobs = {f"{w['name']}:{jid}" for w in wfs for jid in w["jobs"]}
    for k, e in sorted(overrides.items()):
        if k not in all_jobs:
            findings.append(f"STALE {k}: toolchain_overrides names a job that does not exist")
            continue
        for lang in TOOLCHAIN_LANGS:
            if lang in e and lang in cfg.get("toolchain", {}) and norm(e[lang]) == norm(cfg["toolchain"][lang]):
                findings.append(f"REDUNDANT_OVERRIDE {k}: {lang} {e[lang]} equals toolchain.{lang}; delete the override")

    want_go = cfg.get("toolchain", {}).get("go")
    if want_go:
        have = local_go_version()
        if have is None:
            raise Fatal("toolchain.go is set but `go env GOVERSION` could not be read (CANNOT-VERIFY)")
        if norm(have) != norm(want_go):
            findings.append(f"TOOLCHAIN_DRIFT local go is {have}, config pins {want_go}")

    pins = pin_findings(wfs, cfg)
    adopted = cfg.get("adopted")
    in_grace = adopted and (today() - datetime.date.fromisoformat(str(adopted))).days < GRACE_DAYS
    for p in pins:
        (warns if in_grace else findings).append(("WARN " if in_grace else "") + f"PIN_DRIFT {p}")

    notes = []
    if not_local:
        notes.append(f"{len(not_local)} CI job(s) not run locally: {', '.join(not_local)}")
    if unverified:
        notes.append(f"markers unverified (no --gate-log): {', '.join(unverified)}")
    return finish(findings, warns, summary, notes=notes)


# ---------------------------------------------------------------- output / CLI

def finish(findings, warns, summary, notes=(), examined=1):
    for line in findings:
        print(line)
    for line in warns:
        print(line)
    for line in notes:
        print("note: " + line)
    print(summary)
    if not examined:
        print("FAIL: examined 0 uses: lines; a guard that looked at nothing proves nothing")
        return 2
    print(f"{'FAIL' if findings else 'ok'}: {len(findings)} finding(s), {len(warns)} warning(s)")
    return 1 if findings else 0


def default_root():
    p = subprocess.run(["git", "rev-parse", "--show-toplevel"], capture_output=True, text=True)
    return Path(p.stdout.strip()) if p.returncode == 0 and p.stdout.strip() else Path.cwd()


def main(argv=None):
    ap = argparse.ArgumentParser(prog="workflow-preflight", description=__doc__.split("\n")[0])
    ap.add_argument("subcommand", choices=["lint", "parity", "refs", "calls", "dispatch-check"])
    ap.add_argument("--root")
    ap.add_argument("--config")
    ap.add_argument("--actionlint")
    ap.add_argument("--shellcheck")
    ap.add_argument("--gate-log")
    ap.add_argument("--offline", action="store_true", help="lint: skip the tag/SHA network check (prints a warning)")
    args, extra = ap.parse_known_args(argv)
    if args.subcommand in ("refs", "calls", "dispatch-check"):
        print(f"workflow-preflight {args.subcommand}: not built yet (W2)", file=sys.stderr)
        return 2
    if extra:
        print(f"unexpected arguments: {extra}", file=sys.stderr)
        return 2
    root = Path(args.root).resolve() if args.root else default_root()
    try:
        cfg = load_config(Path(args.config) if args.config else root / CONFIG_NAME)
        return {"lint": cmd_lint, "parity": cmd_parity}[args.subcommand](root, cfg, args)
    except Fatal as e:
        print(f"error: {e}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
