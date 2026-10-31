#!/usr/bin/env python3
# VENDORED from claude-config skills/workflow-preflight @ aff54ac09f12 — edit canonical, not this copy
"""workflow-preflight: stop GitHub Actions failures before push (`lint`, `parity`, `refs`, `calls`, `dispatch-check`, `triggers`).

Canonical copy lives in claude-config `skills/workflow-preflight/scripts/`; repos carry a stamped
vendored copy (vendor.sh). Never edit a vendored copy. See SKILL.md for the codes and config.

  workflow_preflight.py lint   [--root R] [--config F] [--actionlint P] [--shellcheck P]
  workflow_preflight.py parity [--root R] [--config F] [--gate-log FILE]
  workflow_preflight.py refs   [--root R] [--config F] [--repo O/R] [--static] [--names-file F] [--gh P]
  workflow_preflight.py dispatch-check -R O/R <workflow.yml> --ref REF [-f k=v ...]   (reads the workflow AT REF)
  workflow_preflight.py triggers [--root R] [--config F]   (CI trigger policy D8; warns unless enforced)
  workflow_preflight.py calls  [--root R] [--repo O/R] [--gh P]   (reusable-workflow contract: ref, inputs, secrets)

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
import os
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
TOP_KEYS = {"tools", "toolchain", "toolchain_overrides", "local_gate", "parity_mode", "jobs", "refs", "network", "adopted",
            "triggers", "trigger_waivers", "test_jobs", "contract_jobs"}
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


def parse_workflow_text(name, text, path=None):
    if yaml is None:
        raise Fatal("PyYAML is required (pip install pyyaml); refusing to guess without a YAML parser")
    label = path or name
    try:
        doc = yaml.safe_load(text)
    except yaml.YAMLError as e:
        raise Fatal(f"{label}: cannot parse: {str(e).splitlines()[0]}")
    if not isinstance(doc, dict):
        raise Fatal(f"{label}: not a workflow mapping")
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
        raise Fatal(f"{label}: 'jobs' is not a mapping")
    return {"name": name, "path": path, "doc": doc, "triggers": triggers, "jobs": jobs, "text": text}


def load_workflows(root):
    if yaml is None:
        raise Fatal("PyYAML is required (pip install pyyaml); refusing to guess without a YAML parser")
    return [parse_workflow_text(f.name, f.read_text(), f) for f in workflow_files(root)]


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

MATRIX_REF_RE = re.compile(r"^\$\{\{\s*matrix\.([A-Za-z0-9_-]+)\s*\}\}$")


def with_values(job, v):
    """[(value, label)] a `with:` input takes. A literal is itself. A pure `${{ matrix.KEY }}` is
    every literal value of KEY in strategy.matrix, `include:` entries too, labelled `(matrix.KEY)`.
    Any other expression cannot be resolved here and yields nothing."""
    if v is None:
        return []
    if "${{" not in str(v):
        return [(v, "")]
    m = MATRIX_REF_RE.match(str(v).strip())
    mx = (job.get("strategy") or {}).get("matrix") if m else None
    if not isinstance(mx, dict):
        return []
    k = m.group(1)
    vals = mx.get(k)
    vals = list(vals) if isinstance(vals, list) else [] if vals is None else [vals]
    vals += [inc[k] for inc in (mx.get("include") or []) if isinstance(inc, dict) and k in inc]
    return [(x, f" (matrix.{k})") for x in vals if "${{" not in str(x)]


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
                    want = ov.get(key, chain.get(key))
                    for v, lbl in with_values(job, (st.get("with") or {}).get(field)) if want is not None else []:
                        drift(f"{w['name']}:{jid}" if key in ov else w["name"], f"{field}{lbl}", v, want)
                if use == "golangci/golangci-lint-action" and "golangci-lint" in tools:
                    for v, lbl in with_values(job, (st.get("with") or {}).get("version")):
                        drift(w["name"], f"golangci-lint version{lbl}", v, tools["golangci-lint"])
                # an action named for the tool (raven-actions/actionlint): its own `version` input
                tool_action = use.rsplit("/", 1)[-1]
                if tool_action in tools and tool_action != "golangci-lint-action":
                    for v, lbl in with_values(job, (st.get("with") or {}).get("version")):
                        if VERSION_RE.search(str(v)):
                            drift(w["name"], f"{tool_action} version{lbl}", VERSION_RE.search(str(v)).group(1),
                                  tools[tool_action])
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


SCRATCH_CHECK = "GIT_CONFIG_GLOBAL=/dev/null git check-ignore -q .scratch/probe"


def scratch_ignore(root):
    """F-036: `.scratch/` must be ignored by the repo's own .gitignore. A push run with
    GIT_CONFIG_GLOBAL=/dev/null drops the global excludes, and laptop_status.sh then refuses the
    push as "tree dirty". Git itself answers, so every pattern form counts. Returns (finding, note)."""
    env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}  # a hook's GIT_DIR names another repo
    env.update(GIT_CONFIG_GLOBAL="/dev/null", GIT_CONFIG_NOSYSTEM="1")

    def git(*a):
        return subprocess.run(["git", "-C", str(root), *a], capture_output=True, text=True, env=env)

    top = git("rev-parse", "--show-toplevel")
    if top.returncode != 0 or Path(top.stdout.strip()).resolve() != Path(root).resolve():
        return None, f"scratch-ignore: skipped, {root} is not a git repo's top level"
    fix = f"add the line `.scratch/` to this repo's .gitignore (check: {SCRATCH_CHECK})"
    if git("check-ignore", "-q", ".scratch/probe").returncode != 0:
        return f"SCRATCH_NOT_IGNORED .scratch/ is not ignored without the global git config; {fix}", None
    src = git("check-ignore", "-v", ".scratch/probe").stdout.split(":", 1)[0]
    if Path(src).name != ".gitignore":
        return f"SCRATCH_NOT_IGNORED .scratch/ is ignored only by {src} (this clone only); {fix}", None
    return None, f"scratch-ignore: .scratch/ ignored by {src}"


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
    finding, note = scratch_ignore(root)
    findings += [finding] if finding else []
    notes += [note] if note else []
    return finish(findings, warns, summary, notes=notes)


# ---------------------------------------------------------------- refs / dispatch-check

# One scan over an expression: a quoted literal is skipped whole (so 'secrets.X' in a string is not a
# reference); otherwise `secrets.X`, `secrets['X']`, `vars.X`, `secrets[<expr>]` or a bare context.
EXPR_RE = re.compile(r"\$\{\{(.*?)\}\}", re.S)
TOKEN_RE = re.compile(r"""'(?:[^']|'')*'|(?<![\w.])(secrets|vars)(?:\.([A-Za-z_]\w*)|\[\s*'([^']*)'\s*\]|(\[)|(?![\w.\[]))""")
GUARD_RE = re.compile(r"""(?<![\w.])(secrets|vars)\.([A-Za-z_]\w*)\s*[!=]=\s*''""")
REF_CONFIG_KEYS = {"optional", "dynamic", "dynamic_env"}
GH_TIMEOUT = 15
SLUG_RE = re.compile(r"""github\.com[:/](?:\d+/)?([^/\s]+)/([^/\s]+?)(?:\.git)?/?$""")


def expression_tokens(text, is_if):
    """Yield (kind, NAME-or-None) for every secrets/vars reference in the expressions of a string."""
    exprs = [m.group(1) for m in EXPR_RE.finditer(text)]
    if is_if and not exprs:
        exprs = [text]
    for expr in exprs:
        for m in TOKEN_RE.finditer(expr):
            if not m.group(1):
                continue
            kind = "secrets" if m.group(1) == "secrets" else "variables"
            name = m.group(2) or m.group(3)
            yield kind, (name.upper() if name else None)


def walk_strings(node, path=()):
    if isinstance(node, dict):
        for k, v in node.items():
            yield from walk_strings(v, path + (str(k),))
    elif isinstance(node, list):
        for i, v in enumerate(node):
            yield from walk_strings(v, path + (i,))
    elif isinstance(node, str):
        yield path, node


def guard_names(cond):
    return {(("secrets" if m.group(1) == "secrets" else "variables"), m.group(2).upper())
            for m in GUARD_RE.finditer(cond)} if isinstance(cond, str) else set()


def validate_refs_config(cfg):
    r = cfg.get("refs", {})
    if not isinstance(r, dict):
        raise Fatal("config 'refs' must be an object")
    bad = sorted(set(r) - REF_CONFIG_KEYS)
    if bad:
        raise Fatal(f"config refs: unknown key(s) {bad}; allowed {sorted(REF_CONFIG_KEYS)}")
    for sect in REF_CONFIG_KEYS:
        if not isinstance(r.get(sect, {}), dict):
            raise Fatal(f"config refs.{sect} must be an object")
    short = []
    for sect in ("optional", "dynamic"):
        for k, reason in r.get(sect, {}).items():
            if not isinstance(reason, str) or len(reason.strip()) < MIN_REASON:
                short.append(f"NO_REASON refs.{sect}['{k}']: needs a reason of at least {MIN_REASON} characters")
    for k, e in r.get("dynamic_env", {}).items():
        ok = isinstance(e, dict) and set(e) == {"values", "reason"} and isinstance(e["values"], list) and e["values"] \
            and all(isinstance(v, str) for v in e["values"])
        if not ok:
            raise Fatal(f"config refs.dynamic_env['{k}'] must be {{\"values\": [env names], \"reason\": ...}}")
        if len(str(e["reason"]).strip()) < MIN_REASON:
            short.append(f"NO_REASON refs.dynamic_env['{k}']: needs a reason of at least {MIN_REASON} characters")
    if short:
        print("\n".join(short))
        raise Fatal(f"{len(short)} refs waiver(s) without a reason")
    return r


def in_grace(cfg):
    adopted = cfg.get("adopted")
    return bool(adopted and (today() - datetime.date.fromisoformat(str(adopted))).days < GRACE_DAYS)


def scan_refs(wfs, rcfg):
    """Static half: every reference with its scope facts. No network."""
    jobs_n, refs, inherit, notes = 0, [], [], []
    for w in wfs:
        callee_only = w["triggers"] == {"workflow_call"}
        on = w["doc"].get("on", w["doc"].get(True))
        wc = on.get("workflow_call") if isinstance(on, dict) else None
        declared = {str(k).upper() for k in (wc.get("secrets") or {})} if isinstance(wc, dict) and isinstance(wc.get("secrets"), dict) else set()
        units = [(None, {k: v for k, v in w["doc"].items() if k != "jobs"})]
        units += [(jid, job) for jid, job in w["jobs"].items() if isinstance(job, dict)]
        for jid, job in units:
            jobs_n += jid is not None
            calling = jid is not None and isinstance(job.get("uses"), str)
            env, env_expr = None, False
            if jid is not None and not calling:
                env = job.get("environment")
                env = env.get("name") if isinstance(env, dict) else env
                env_expr = isinstance(env, str) and "${{" in env
                env = None if env_expr or env is None else str(env)
            if calling and job.get("secrets") == "inherit":
                inherit.append((w["name"], jid, job["uses"]))
            jg = guard_names(job.get("if")) if jid is not None else set()
            for path, text in walk_strings(job):
                step = path[1] if path[:1] == ("steps",) and len(path) > 1 and isinstance(path[1], int) else None
                g = set(jg)
                if step is not None and isinstance(job["steps"][step], dict):
                    g |= guard_names(job["steps"][step].get("if"))
                for kind, name in expression_tokens(text, path[-1] == "if"):
                    refs.append({"wf": w["name"], "job": jid, "kind": kind, "name": name, "env": env,
                                 "env_expr": env_expr, "calling": calling, "callee_only": callee_only,
                                 "slot": path[0] if path else None, "optional": (kind, name) in g,
                                 "declared": kind == "secrets" and name in declared,
                                 "pr": bool(w["triggers"] & {"pull_request", "pull_request_target"})})
    named = sum(1 for r in refs if r["kind"] == "secrets" and r["name"] != "GITHUB_TOKEN") + len(inherit)
    return {"refs": refs, "jobs": jobs_n, "inherit": inherit, "secrets_named": named, "notes": notes}


class Names:
    """Secret/variable NAMES by scope. Upper-cased: GitHub treats names case-insensitively."""

    def __init__(self, repo, org, envs, dependabot=None):
        self.repo, self.org, self.envs, self.dependabot = repo, org, envs, dependabot

    def base(self, kind):
        return self.repo[kind] | self.org[kind]

    def env(self, e, kind):
        return self.envs.get(e, {}).get(kind, set())

    def describe(self):
        envs = ", ".join(f"{e} {len(v['secrets'])}/{len(v['variables'])}" for e, v in sorted(self.envs.items()))
        return (f"repo {len(self.repo['secrets'])}/{len(self.repo['variables'])}, "
                f"org-visible {len(self.org['secrets'])}/{len(self.org['variables'])}, "
                f"envs {len(self.envs)}[{envs}]")


def _pair(names_by_kind):
    return {k: {str(n).upper() for n in names_by_kind.get(k, [])} for k in ("secrets", "variables")}


def names_from_fixture(path, need_dependabot):
    try:
        d = json.loads(Path(path).read_text())
    except (OSError, ValueError) as e:
        raise Fatal(f"--names-file {path}: {e}")
    errors = d.get("errors", {})

    def get(key, items):
        if key in errors:
            raise Fatal(f"CANNOT-VERIFY: names for {key}: HTTP {errors[key]}")
        return [i["name"] if isinstance(i, dict) else i for i in items]  # project the name: never a value

    repo = _pair({k: get(f"repo.{k}", d.get("repo", {}).get(k, [])) for k in ("secrets", "variables")})
    org = _pair({k: get(f"org.{k}", d.get("org", {}).get(k, [])) for k in ("secrets", "variables")})
    get("environments", [])
    envs = {e: _pair({k: get(f"env:{e}.{k}", v.get(k, [])) for k in ("secrets", "variables")})
            for e, v in d.get("environments", {}).items()}
    dep = None
    if need_dependabot and "dependabot" in d:
        dep = {str(n).upper() for n in get("dependabot", d["dependabot"])}
    return Names(repo, org, envs, dep)


def names_from_gh(gh, repo, need_dependabot):
    from concurrent.futures import ThreadPoolExecutor
    from urllib.parse import quote

    def call(path, jq, upper=True):
        try:
            p = subprocess.run([gh, "api", "--paginate", path, "--jq", jq], capture_output=True, text=True,
                               timeout=GH_TIMEOUT)
        except OSError as e:
            raise Fatal(f"CANNOT-VERIFY: cannot run gh ({gh}): {e}")
        except subprocess.TimeoutExpired:
            raise Fatal(f"CANNOT-VERIFY: GET {path}: no answer within {GH_TIMEOUT}s")
        if p.returncode != 0:
            err = (p.stderr.strip().splitlines() or ["failed"])[0]
            raise Fatal(f"CANNOT-VERIFY: GET {path}: {err}")
        return [(ln.strip().upper() if upper else ln.strip()) for ln in p.stdout.splitlines() if ln.strip()]

    base = f"repos/{repo}"
    with ThreadPoolExecutor(max_workers=8) as ex:
        fixed = {"rs": f"{base}/actions/secrets?per_page=100", "rv": f"{base}/actions/variables?per_page=100",
                 "os": f"{base}/actions/organization-secrets?per_page=100",
                 "ov": f"{base}/actions/organization-variables?per_page=100"}
        jq = {"rs": ".secrets[].name", "rv": ".variables[].name", "os": ".secrets[].name", "ov": ".variables[].name"}
        futs = {k: ex.submit(call, p, jq[k]) for k, p in fixed.items()}
        fenv = ex.submit(call, f"{base}/environments?per_page=100", ".environments[].name", False)
        fdep = ex.submit(call, f"{base}/dependabot/secrets?per_page=100", ".secrets[].name") if need_dependabot else None
        got = {k: f.result() for k, f in futs.items()}
        env_names = fenv.result()  # environment names are not upper-cased: they are URL parts
        efuts = {(e, k): ex.submit(call, f"{base}/environments/{quote(e, safe='')}/{k}?per_page=100", f".{k}[].name")
                 for e in env_names for k in ("secrets", "variables")}
        envs = {e: {"secrets": set(efuts[(e, "secrets")].result()), "variables": set(efuts[(e, "variables")].result())}
                for e in env_names}
        dep = None
        if fdep:
            try:
                dep = set(fdep.result())
            except Fatal:
                dep = None
    return Names({"secrets": set(got["rs"]), "variables": set(got["rv"])},
                 {"secrets": set(got["os"]), "variables": set(got["ov"])}, envs, dep)


def repo_slug(root):
    p = subprocess.run(["git", "-C", str(root), "config", "--get", "remote.origin.url"], capture_output=True, text=True)
    m = SLUG_RE.search(p.stdout.strip()) if p.returncode == 0 else None
    return ("%s/%s" % m.groups()) if m else None


def classify(ref, names, envname):
    """None when the reference resolves, else the finding code."""
    n, kind = ref["name"], ref["kind"]
    if n in names.base(kind) or (envname and n in names.env(envname, kind)):
        return None
    elsewhere = [e for e in names.envs if e != envname and n in names.env(e, kind)]
    if not elsewhere:
        return "MISSING"
    if ref["calling"]:
        return "UNPROVEN" if kind == "secrets" and ref["slot"] == "secrets" else "ENV_ONLY_AT_CALLER"
    return "ENV_MISMATCH"


def evaluate_refs(scan, rcfg, cfg, names, repo, static):
    grace = in_grace(cfg)
    findings, warns, notes = {}, {}, []
    cnt = dict(S=0, V=0, D=0, O=0, T=0, K=0)

    def add(code, where, msg, graded=False):
        line = f"{code} {where}: {msg}"
        # UNPROVEN always warns (owner ruling 2026-10-21): no deadline escalation
        bucket = warns if code == "UNPROVEN" or (graded and grace) else findings
        bucket[("WARN " if bucket is warns else "") + line] = 1

    seen_env = set()
    for r in scan["refs"]:
        where = f"{r['wf']}:{r['job']}" if r["job"] else f"{r['wf']} (workflow level)"
        label = f"{'secrets' if r['kind'] == 'secrets' else 'vars'}.{r['name']}" if r["name"] else \
            f"{'secrets' if r['kind'] == 'secrets' else 'vars'}[<expr>]"
        if r["kind"] == "secrets" and r["name"] == "GITHUB_TOKEN":
            cnt["T"] += 1
            continue
        if r["callee_only"]:
            cnt["K"] += 1
            continue
        if r["env_expr"] and (where not in seen_env):
            seen_env.add(where)
            spec = rcfg.get("dynamic_env", {}).get(where)
            cnt["D"] += 1
            if not spec:
                add("DYNAMIC_ENV", where, "environment is an expression; list its possible values in refs.dynamic_env", True)
        if r["name"] is None:
            cnt["D"] += 1
            if where not in rcfg.get("dynamic", {}):
                add("DYNAMIC", where, f"{label} cannot be resolved statically; waive it in refs.dynamic with a reason", True)
            continue
        if r["optional"] or r["name"] in {k.upper() for k in rcfg.get("optional", {})}:
            cnt["O"] += 1
            continue
        cnt["S" if r["kind"] == "secrets" else "V"] += 1
        if r["declared"] or static or names is None:
            continue
        if r["env_expr"]:
            spec = rcfg.get("dynamic_env", {}).get(where)
            envs = spec["values"] if spec else [None]
        else:
            envs = [r["env"]]
        for e in envs:
            code = classify(r, names, e)
            if code == "ENV_MISMATCH" and r["env_expr"] and e is None:
                continue  # an unwaived expression environment is already reported as DYNAMIC_ENV
            if not code:
                continue
            at = f"environment {e}" if e else "no environment"
            if r["env"] is None and not e and r["calling"]:
                at = "the calling job (no environment)"
            if e and e not in names.envs:
                at += f" (no such environment among {sorted(names.envs)})"
            in_envs = sorted(x for x in names.envs if r["name"] in names.env(x, r["kind"]))
            msg = {"MISSING": f"{label} is in no scope this job can read (repo, org-visible, {at})",
                   "ENV_MISMATCH": f"{label} exists only in environment(s) {in_envs}; this job runs in {at}",
                   "ENV_ONLY_AT_CALLER": f"{label} exists only in environment(s) {in_envs}; a calling job has no environment, so it is empty here",
                   "UNPROVEN": f"{label} is an environment secret passed by name into a reusable workflow (in {in_envs}); GitHub documents it, no fleet run has proven it"}[code]
            add(code, where, msg, graded=(code == "UNPROVEN"))
        if r["pr"] and r["kind"] == "secrets" and names.dependabot is not None and r["name"] not in names.dependabot:
            warns[f"WARN DEPENDABOT_GAP {r['wf']}: {label} is not in Dependabot's secret store; a Dependabot PR gets it empty"] = 1
    for wfname, jid, uses in scan["inherit"]:
        if uses.startswith("./") or not repo:
            if not repo and not uses.startswith("./"):
                notes.append(f"caller org unknown (pass --repo O/R): INHERIT_CROSS_ORG not evaluated for {wfname}:{jid}")
            continue
        if uses.split("/")[0].lower() != repo.split("/")[0].lower():
            add("INHERIT_CROSS_ORG", f"{wfname}:{jid}",
                f"`secrets: inherit` into {uses.split('@')[0]} crosses orgs; inherit passes nothing there, pass each secret by name")
    return list(findings), list(warns), notes, cnt


def refs_summary(wfs, scan, cnt, names):
    R = sum(cnt[k] for k in "SVDOTK")
    s = (f"examined: {len(wfs)} workflows, {scan['jobs']} jobs, {R} references ({cnt['S']} secrets, {cnt['V']} vars, "
         f"{cnt['D']} dynamic, {cnt['O']} optional, {cnt['T']} GITHUB_TOKEN)")
    if cnt["K"]:
        s += f"; {cnt['K']} in callee-only workflows (not resolved here)"
    s += f"; live names: {names.describe()}" if names else "; static: no names fetched"
    return s, R


def run_refs(wfs, cfg, repo, loader, static, announce=False):
    """Shared by `refs` and `dispatch-check`. Returns (findings, warns, notes, summary)."""
    rcfg = validate_refs_config(cfg)
    scan = scan_refs(wfs, rcfg)
    if announce:
        print(f"secrets-named: {scan['secrets_named']}")
    if not wfs or not scan["jobs"]:
        print(f"examined: {len(wfs)} workflows, {scan['jobs']} jobs")
        raise Fatal("0 workflows/jobs examined; a check that looked at nothing proves nothing")
    names = None if static else loader(any(r["pr"] for r in scan["refs"]))
    findings, warns, notes, cnt = evaluate_refs(scan, rcfg, cfg, names, repo, static)
    summary, R = refs_summary(wfs, scan, cnt, names)
    if R == 0 and any(re.search(r"\b(?:secrets|vars)\.", w["text"]) for w in wfs):
        print(summary)
        raise Fatal("parser self-check: 0 references found although a workflow's text holds `secrets.` or `vars.` "
                    "(a reference outside ${{ }} or if:, or a parser gap); refusing to call this clean")
    return findings, warns, notes, summary


def find_gh(args):
    gh = args.gh or os.environ.get("WORKFLOW_PREFLIGHT_GH") or shutil.which("gh")
    if not gh:
        raise Fatal("CANNOT-VERIFY: gh is not installed")
    return gh


def names_loader(args, repo):
    if args.names_file:
        return lambda dep: names_from_fixture(args.names_file, dep)
    if not repo:
        raise Fatal("cannot tell which repo to read names from: pass --repo O/R (or --names-file / --static)")
    gh = find_gh(args)
    return lambda dep: names_from_gh(gh, repo, dep)


def cmd_refs(root, args):
    cfg_path = Path(args.config) if args.config else root / CONFIG_NAME
    cfg = load_config(cfg_path) if cfg_path.is_file() else {}
    repo = args.repo or repo_slug(root)
    wfs = load_workflows(root)
    loader = None if args.static else names_loader(args, repo)
    findings, warns, notes, summary = run_refs(wfs, cfg, repo, loader, args.static)
    return finish(findings, warns, summary, notes=notes + ([] if cfg else [f"no {CONFIG_NAME}: no refs waivers or grace in effect"]),
                  examined=len(wfs), what="workflows")


def fetch_remote_workflow(gh, repo, wf, ref):
    from urllib.parse import quote
    path = f"repos/{repo}/contents/.github/workflows/{wf}" + (f"?ref={quote(ref, safe='')}" if ref else "")
    try:
        p = subprocess.run([gh, "api", "-H", "Accept: application/vnd.github.raw+json", path],
                           capture_output=True, text=True, timeout=GH_TIMEOUT)
    except OSError as e:
        raise Fatal(f"CANNOT-VERIFY: cannot run gh ({gh}): {e}")
    except subprocess.TimeoutExpired:
        raise Fatal(f"CANNOT-VERIFY: GET {path}: no answer within {GH_TIMEOUT}s")
    if p.returncode != 0:
        raise Fatal(f"CANNOT-VERIFY: cannot read {wf} at ref {ref or '(default branch)'} of {repo}: "
                    f"{(p.stderr.strip().splitlines() or ['failed'])[0]}")
    return p.stdout


def dispatch_input_findings(w, pairs):
    on = w["doc"].get("on", w["doc"].get(True))
    if not (isinstance(on, dict) and "workflow_dispatch" in on) and "workflow_dispatch" not in w["triggers"]:
        return [f"NOT_DISPATCHABLE {w['name']}: no workflow_dispatch trigger at this ref; GitHub will answer 422"]
    spec = on.get("workflow_dispatch") if isinstance(on, dict) else None
    inputs = (spec or {}).get("inputs") or {} if isinstance(spec, dict) or spec is None else {}
    out = []
    for k in sorted(set(pairs) - set(inputs)):
        out.append(f"UNKNOWN_DISPATCH_INPUT {w['name']}: -f {k}=... is not declared under workflow_dispatch.inputs {sorted(inputs)}")
    for k, d in inputs.items():
        d = d if isinstance(d, dict) else {}
        if k not in pairs and d.get("required") and "default" not in d:
            out.append(f"MISSING_DISPATCH_INPUT {w['name']}: required input '{k}' is not passed with -f")
        if k in pairs and d.get("type") == "choice" and pairs[k] not in [str(o) for o in d.get("options", [])]:
            out.append(f"BAD_CHOICE {w['name']}: input '{k}'={pairs[k]!r} is not one of {d.get('options')}")
    return out


def cmd_dispatch_check(root, args):
    if not (args.repo and args.workflow):
        raise Fatal("usage: dispatch-check -R O/R <workflow.yml> --ref REF [-f k=v ...]")
    wf = args.workflow.rsplit("/", 1)[-1]
    if not wf.endswith((".yml", ".yaml")):
        raise Fatal(f"CANNOT-VERIFY: give the workflow FILE name (x.yml), not {args.workflow!r}")
    pairs = {}
    for kv in args.field or []:
        if "=" not in kv:
            raise Fatal(f"-f needs k=v, got {kv!r}")
        pairs[kv.split("=", 1)[0]] = kv.split("=", 1)[1]
    gh = find_gh(args)
    text = fetch_remote_workflow(gh, args.repo, wf, args.ref)
    w = parse_workflow_text(wf, text)
    cfg = {}
    cfg_path = Path(args.config) if args.config else root / CONFIG_NAME
    if cfg_path.is_file() and (args.config or (repo_slug(root) or "").lower() == args.repo.lower()):
        cfg = load_config(cfg_path)
    loader = names_loader(args, args.repo)
    findings, warns, notes, summary = run_refs([w], cfg, args.repo, loader, args.static, announce=True)
    findings = dispatch_input_findings(w, pairs) + findings
    notes.append("calls: not built yet (W3); callee contracts are not checked")
    return finish(findings, warns, summary, notes=notes, examined=1, what="workflows")


# ---------------------------------------------------------------- calls

class CalleeUnavailable(Exception):
    """kind 'bad_ref': the repo answers but the ref does not exist (a finding).
    kind 'cannot': offline, no gh, private cross-org 404 (CANNOT-VERIFY, exit 2). Tests replace the fetchers."""

    def __init__(self, kind, msg):
        super().__init__(msg)
        self.kind = kind


def _gh_api(gh, path, jq=None, raw=False):
    if not gh:
        raise CalleeUnavailable("cannot", "gh is not installed")
    cmd = [gh, "api"] + (["-H", "Accept: application/vnd.github.raw+json"] if raw else []) + [path] + (["--jq", jq] if jq else [])
    try:
        p = subprocess.run(cmd, capture_output=True, text=True, timeout=GH_TIMEOUT)
    except OSError as e:
        raise CalleeUnavailable("cannot", f"cannot run gh ({gh}): {e}")
    except subprocess.TimeoutExpired:
        raise CalleeUnavailable("cannot", f"GET {path}: no answer within {GH_TIMEOUT}s")
    return p


SHA_RE = re.compile(r"[0-9a-f]{40}")
TAG_OBJ_JQ = '.object.type + " " + .object.sha'


def _unreadable(gh, repo, ref, why):
    """`ref` did not resolve: a finding when the repo itself answers, else CANNOT-VERIFY."""
    if _gh_api(gh, f"repos/{repo}", jq=".full_name").returncode == 0:
        return CalleeUnavailable("bad_ref", f"{ref} does not resolve in {repo} ({why})")
    return CalleeUnavailable("cannot", f"cannot read {repo} ({why}); private or offline")


def resolve_ref(gh, repo, ref):
    """Commit SHA of `ref` in `repo`. A 40-hex ref is its own SHA (the fetch proves it exists). A
    tag-shaped ref goes through `git/ref/tags/REF` (spec 3.4), so a branch named like a tag is not taken
    for the tag; annotated tags are dereferenced to their commit. Tests replace this function."""
    from urllib.parse import quote
    if SHA_RE.fullmatch(ref):
        return ref
    if re.fullmatch(r"v\d[\w.\-]*", ref):
        p = _gh_api(gh, f"repos/{repo}/git/ref/tags/{quote(ref, safe='')}", jq=TAG_OBJ_JQ)
        for _ in range(5):  # tag object -> ... -> commit
            kind, _, sha = p.stdout.strip().partition(" ")
            if p.returncode != 0 or not SHA_RE.fullmatch(sha):
                break
            if kind == "commit":
                return sha
            if kind != "tag":
                break
            p = _gh_api(gh, f"repos/{repo}/git/tags/{sha}", jq=TAG_OBJ_JQ)
    else:
        p = _gh_api(gh, f"repos/{repo}/commits/{quote(ref, safe='')}", jq=".sha")
        if p.returncode == 0 and SHA_RE.fullmatch(p.stdout.strip()):
            return p.stdout.strip()
    raise _unreadable(gh, repo, ref, (p.stderr.strip().splitlines() or ["failed"])[0])


def fetch_callee_text(gh, repo, path, sha):
    """Raw text of `path` in `repo` at commit `sha`. A failure where `sha` is not a commit of a readable
    repo is a BAD_REF (a typo'd pin), anything else CANNOT-VERIFY. Tests replace this function."""
    p = _gh_api(gh, f"repos/{repo}/contents/{path}?ref={sha}", raw=True)
    if p.returncode != 0:
        why = (p.stderr.strip().splitlines() or ["failed"])[0]
        if _gh_api(gh, f"repos/{repo}/commits/{sha}", jq=".sha").returncode != 0:
            raise _unreadable(gh, repo, sha, why)
        raise CalleeUnavailable("cannot", f"cannot read {path} at {sha[:12]} of {repo}: {why}")
    return p.stdout


def job_uses_lines(text):
    """[(job id, raw line, uses value)] for each job-level `uses:` of a reusable workflow, read from the raw
    text (indentation-scoped, so `uses:` inside a `run: |` block or a step is not one). A flow-style job
    (`a: {uses: ...}`) is not seen, which the calls self-check reports as a count mismatch."""
    out, in_jobs, jind, jid, cind = [], False, None, None, None
    for ln in text.splitlines():
        s = ln.strip()
        if not s or s.startswith("#"):
            continue
        ind = len(ln) - len(ln.lstrip())
        if ind == 0:
            in_jobs, jind, jid = s.startswith("jobs:"), None, None
            continue
        if not in_jobs:
            continue
        if jind is None:
            jind = ind
        if ind == jind:
            m = re.match(r"['\"]?([^'\":\s]+)['\"]?\s*:", s)
            jid, cind = (m.group(1) if m else None), None
            continue
        if jid is None:
            continue
        if cind is None:
            cind = ind
        m = USES_RE.match(ln) if ind == cind and not s.startswith("-") else None
        if m and ".github/workflows/" in m.group(1):
            out.append((jid, ln, m.group(1)))
    return out


YAML11_BOOL_WORDS = {"yes", "no", "on", "off"}


def raw_with_strings(text):
    """{job id: {input name: raw text}} for `with:` values PyYAML (YAML 1.1) read as bool from yes/no/on/off.
    GitHub reads those as strings, so the literal check must see the string, not the bool."""
    try:
        root = yaml.compose(text)
    except yaml.YAMLError:
        return {}
    out = {}
    jobs = next((v for k, v in root.value if getattr(k, "value", None) == "jobs"), None) if isinstance(root, yaml.MappingNode) else None
    if not isinstance(jobs, yaml.MappingNode):
        return out
    for jk, jv in jobs.value:
        w = next((v for k, v in jv.value if getattr(k, "value", None) == "with"), None) if isinstance(jv, yaml.MappingNode) else None
        if isinstance(w, yaml.MappingNode):
            out[jk.value] = {k.value: v.value for k, v in w.value
                             if isinstance(v, yaml.ScalarNode) and v.tag == "tag:yaml.org,2002:bool"
                             and v.value.lower() in YAML11_BOOL_WORDS}
    return out


def reusable_calls(wfs):
    """Every job-level `uses:` that names a reusable workflow, with the tag comment on ITS OWN line."""
    out = []
    for w in wfs:
        lines = job_uses_lines(w["text"])
        raw = raw_with_strings(w["text"])
        for jid, job in w["jobs"].items():
            u = job.get("uses") if isinstance(job, dict) else None
            if not isinstance(u, str) or ".github/workflows/" not in u:
                continue
            comment = None
            for j, ln, v in lines:
                if j == str(jid) and v == u:
                    c = TAG_COMMENT_RE.search(ln)
                    comment = c.group(1) if c else None
                    break
            wth = dict(job.get("with")) if isinstance(job.get("with"), dict) else {}
            for k, s in raw.get(str(jid), {}).items():
                if k in wth:
                    wth[k] = s
            out.append({"wf": w["name"], "job": jid, "uses": u, "comment": comment,
                        "with": wth, "secrets": job.get("secrets")})
    return out


def literal_type_ok(declared, v):
    if isinstance(v, str) and "${{" in v:
        return True
    if declared == "boolean":
        return isinstance(v, bool) or (isinstance(v, str) and v in ("true", "false"))
    if declared == "number":
        if isinstance(v, bool):
            return False
        if isinstance(v, (int, float)):
            return True
        try:
            float(v)
            return True
        except (TypeError, ValueError):
            return False
    return isinstance(v, str)  # string (the default when `type` is absent)


def contract_of(text, label):
    w = parse_workflow_text(label, text)
    on = w["doc"].get("on", w["doc"].get(True))
    wc = on.get("workflow_call") if isinstance(on, dict) else None
    if "workflow_call" not in w["triggers"]:
        return None
    wc = wc if isinstance(wc, dict) else {}
    ins = wc.get("inputs") if isinstance(wc.get("inputs"), dict) else {}
    sec = wc.get("secrets") if isinstance(wc.get("secrets"), dict) else {}
    return {"inputs": {str(k): (v if isinstance(v, dict) else {}) for k, v in ins.items()},
            "secrets": {str(k): (v if isinstance(v, dict) else {}) for k, v in sec.items()}}


def cmd_calls(root, args):
    wfs = load_workflows(root)
    caller_org = ((args.repo or repo_slug(root) or "").split("/")[0]).lower()
    gh = args.gh or os.environ.get("WORKFLOW_PREFLIGHT_GH") or shutil.which("gh")
    calls = reusable_calls(wfs)
    findings, warns, notes = [], [], []
    contracts, cache_dir = {}, root / ".scratch" / "workflow-preflight"
    n_local = n_remote = 0
    tag_cache = {}
    for c in calls:
        where = f"{c['wf']}:{c['job']}"
        uses = c["uses"]
        path, _, ref = uses.partition("@")
        if path.startswith("./"):
            n_local += 1  # local callees are actionlint's job (`lint`), not this command's
            continue
        n_remote += 1
        parts = path.split("/")
        repo = "/".join(parts[:2])
        fpath = "/".join(parts[2:])
        callee_org = parts[0].lower()
        is_org = (path.lower() + "/").startswith(ORG_REPO + "/")
        is_sha = bool(re.fullmatch(r"[0-9a-f]{40}", ref))
        if is_org and ref == "v1":
            findings.append(f"LEGACY_ALIAS {where}: {uses} pins the frozen v1 alias; pin an exact vX.Y.Z")
        elif is_org and not (TAG_RE.fullmatch(ref) or is_sha):
            findings.append(f"UNPINNED {where}: {uses} must be an exact tag vX.Y.Z or a 40-hex SHA with a # vX.Y.Z comment")
        elif not is_org and not (is_sha or re.fullmatch(r"v\d+(\.\d+){0,2}", ref)):
            findings.append(f"UNPINNED {where}: {uses} is not a tag or a full commit SHA")
        if is_org and is_sha and c["comment"]:
            try:
                if c["comment"] not in tag_cache:
                    tag_cache[c["comment"]] = remote_tag_sha(c["comment"])
                if tag_cache[c["comment"]] != ref:
                    findings.append(f"TAG_SHA_MISMATCH {where}: {c['comment']} "
                                    + (f"points at {tag_cache[c['comment']]}" if tag_cache[c["comment"]] else "does not exist")
                                    + f", but the pin is {ref}")
            except NetworkUnavailable as e:
                warns.append(f"WARNING: tag/SHA NOT verified (offline) {where}: {e}")
        try:
            sha = resolve_ref(gh, repo, ref)
            key = (repo, sha, fpath)
            cf = cache_dir / ("__".join([repo.replace("/", "__"), sha, fpath.replace("/", "__")]))
            text = None
            if cf.is_file():
                text = cf.read_text()
                try:
                    parse_workflow_text(cf.name, text)
                except Fatal:
                    text = None  # a torn or corrupt cache file is ignored and refetched
            if text is None:
                text = fetch_callee_text(gh, repo, fpath, sha)
                cache_dir.mkdir(parents=True, exist_ok=True)
                tmp = cf.with_name(cf.name + f".tmp{os.getpid()}")
                tmp.write_text(text)
                os.replace(tmp, cf)
        except CalleeUnavailable as e:
            if e.kind == "bad_ref":
                findings.append(f"BAD_REF {where}: {e}")
            else:
                findings.append(f"CANNOT-VERIFY {where}: {uses}: {e}")
            continue
        if key not in contracts:
            contracts[key] = contract_of(text, str(path))
        k = contracts[key]
        if k is None:
            findings.append(f"NOT_CALLABLE {where}: {uses} has no on.workflow_call trigger")
            continue
        ins = {n.lower(): (n, d) for n, d in k["inputs"].items()}
        passed = {str(n).lower(): (n, v) for n, v in c["with"].items()}
        for low, (n, v) in passed.items():
            if low not in ins:
                findings.append(f"UNKNOWN_INPUT {where}: with.{n} is not declared by {uses.split('@')[0]} (declares {sorted(k['inputs'])})")
            elif not literal_type_ok(ins[low][1].get("type", "string"), v):
                findings.append(f"INPUT_TYPE {where}: with.{n}={v!r} is a {type(v).__name__} literal, the callee declares {ins[low][1].get('type', 'string')}")
        for low, (n, d) in ins.items():
            if d.get("required") and "default" not in d and low not in passed:
                findings.append(f"MISSING_INPUT {where}: required input '{n}' is not passed to {uses.split('@')[0]}")
        sec = c["secrets"]
        required = [n for n, d in k["secrets"].items() if d.get("required")]
        if sec == "inherit":
            if required and callee_org != caller_org:
                if caller_org:
                    findings.append(f"INHERIT_CROSS_ORG {where}: `secrets: inherit` into {uses.split('@')[0]} crosses orgs and passes nothing; "
                                    f"pass the required secret(s) {sorted(required)} by name")
                else:
                    findings.append(f"CANNOT-VERIFY {where}: caller org unknown, so `secrets: inherit` into "
                                    f"{uses.split('@')[0]} (requires {sorted(required)}) cannot be judged; pass --repo O/R")
            continue
        given = {str(n).upper(): n for n in sec} if isinstance(sec, dict) else {}
        decl = {n.upper() for n in k["secrets"]}
        for up, n in given.items():
            if up not in decl:
                findings.append(f"UNKNOWN_SECRET {where}: secrets.{n} is not declared by {uses.split('@')[0]} (declares {sorted(k['secrets'])})")
        for n in required:
            if n.upper() not in given:
                findings.append(f"MISSING_SECRET {where}: required secret '{n}' is not passed to {uses.split('@')[0]} (and not inherited within one org)")
    summary = (f"examined {len(calls)} calls ({n_remote} remote checked, {n_local} local left to actionlint), "
               f"{len(contracts)} callee contracts")
    raw_lines = sum(len(job_uses_lines(w["text"])) for w in wfs)
    if raw_lines != len(calls):
        findings.append(f"CANNOT-VERIFY self-check: {raw_lines} uses: lines of reusable workflows in the raw text but "
                        f"{len(calls)} calls parsed (duplicate job ids, or a flow-style job); the check cannot be trusted")
    rc = finish(findings, warns, summary, notes=notes, examined=(len(wfs) and (len(calls) or not raw_lines)), what="calls")
    if any(f.startswith("CANNOT-VERIFY") for f in findings):
        return 2
    return rc


# ---------------------------------------------------------------- triggers (ci-trigger-policy D8)

TRIGGER_WAIVER_CLASSES = {"outward", "no-laptop", "cache", "release"}
MAIN_BRANCHES = {"main", "master", "*", "**"}
DEPLOY_FILE_RE = re.compile(r"^(deploy|publish|release)[^/]*\.ya?ml$")
BUILD_RE = re.compile(r"\bdocker(?:\s+buildx)?\s+build\b|\bbuildx\s+build\b|\bbuild_only\b")
BUILD_ACTIONS = ("docker/build-push-action", "docker/bake-action")
DEPLOY_RUN_RE = re.compile(
    r"\bgcloud\s+run\s+(?:deploy|services\s+update)\b|\bgcloud\s+(?:app|functions)\s+deploy\b"
    r"|\bnpm\s+publish\b|\btwine\s+upload\b|\bdocker\s+push\b|\bgh\s+release\s+create\b"
    r"|\bkubectl\s+(?:apply|set\s+image|rollout)\b|\bhelm\s+(?:upgrade|install)\b"
    r"|\bterraform\s+apply\b|\bwrangler\s+(?:deploy|publish|pages\s+deploy)\b|\bfirebase\s+deploy\b"
    r"|\bcdk\s+deploy\b|\bcargo\s+publish\b|\bmvn\b.*\bdeploy\b")
DEPLOY_ACTIONS = ("pypi-publish", "google-github-actions/deploy-cloudrun", "cloudflare/wrangler-action",
                  "cloudflare/pages-action", "softprops/action-gh-release")
AWAIT_RE = re.compile(r"\bawait\b|wait-on-check|wait-for-check", re.I)
RULE = {"PUSH_MAIN": "rules 3/5/9", "NO_PATHS": "rule 9", "SCHEDULE": "rule 8", "PR_AND_PUSH": "rule 1",
        "DEPLOY_NO_TEST": "rule 10", "CONTRACT_PATHS": "rule 7", "WAIVER_INVALID": "rule 9"}


def validate_triggers_config(cfg):
    errors = []
    t = cfg.get("triggers", {})
    if not isinstance(t, dict) or set(t) - {"enforce"} or not isinstance(t.get("enforce", False), bool):
        errors.append("triggers must be an object whose only key is 'enforce' (boolean)")
    if not isinstance(cfg.get("trigger_waivers", {}), dict):
        errors.append("trigger_waivers must be an object")
    for k in ("test_jobs", "contract_jobs"):
        v = cfg.get(k, [])
        if not (isinstance(v, list) and all(isinstance(x, str) and ":" in x for x in v)):
            errors.append(f"{k} must be a list of '<workflow file>:<job id>' strings")
    return errors


def on_events(w):
    """The workflow's `on:` as {event: config-dict}; list/str forms and `event:` with no body give {}."""
    on = w["doc"].get("on", w["doc"].get(True))
    if isinstance(on, str):
        return {on: {}}
    if isinstance(on, list):
        return {str(e): {} for e in on}
    if isinstance(on, dict):
        return {str(e): (v if isinstance(v, dict) else {}) for e, v in on.items()}
    return {}


def pushes_main(events):
    if "push" not in events:
        return False
    p = events["push"]
    br = p.get("branches")
    if br is None:
        return "tags" not in p and "tags-ignore" not in p  # bare `push:` fires on every branch, main included
    br = [br] if isinstance(br, str) else br
    return any(str(b) in MAIN_BRANCHES for b in br)


def has_paths(cfgd):
    return bool(cfgd.get("paths") or cfgd.get("paths-ignore"))


def job_strings(job):
    out = []
    for st in steps_of(job):
        out += step_strings(st)
    if isinstance(job.get("with"), dict):
        out += [str(v) for k, v in job["with"].items()] + [str(k) for k in job["with"]]
    return out


def is_build_job(job):
    uses = [str(st.get("uses", "")) for st in steps_of(job)] + [str(job.get("uses", ""))]
    return any(u.startswith(BUILD_ACTIONS) for u in uses) or any(BUILD_RE.search(s) for s in job_strings(job))


def is_deploy_job(job):
    uses = [str(st.get("uses", "")) for st in steps_of(job)]
    return any(DEPLOY_RUN_RE.search(ln) for ln in run_lines(job)) or any(
        d in u for u in uses for d in DEPLOY_ACTIONS)


def has_await(job):
    for st in steps_of(job):
        if AWAIT_RE.search(" ".join([str(st.get("name", "")), str(st.get("uses", ""))] + step_strings(st))):
            return True
    return False


def needs_of(job):
    n = job.get("needs") if isinstance(job, dict) else None
    return [n] if isinstance(n, str) else [str(x) for x in n] if isinstance(n, list) else []


def reaches(jobs, jid, targets, seen=None):
    seen = seen if seen is not None else set()
    for n in needs_of(jobs[jid]):
        if n in seen or n not in jobs:
            continue
        seen.add(n)
        if n in targets or reaches(jobs, n, targets, seen):
            return True
    return False


def check_waiver(wf, wv):
    if not isinstance(wv, dict):
        return f"{wf}: waiver must be an object"
    if set(wv) - {"class", "paths", "reason"}:
        return f"{wf}: unknown waiver key(s) {sorted(set(wv) - {'class', 'paths', 'reason'})}"
    if wv.get("class") not in TRIGGER_WAIVER_CLASSES:
        return f"{wf}: class must be one of {sorted(TRIGGER_WAIVER_CLASSES)}"
    if not (isinstance(wv.get("paths"), list) and wv["paths"] and all(isinstance(p, str) for p in wv["paths"])):
        return f"{wf}: paths must be a non-empty list of globs"
    if len(str(wv.get("reason", "")).strip()) < MIN_REASON:
        return f"{wf}: reason needs at least {MIN_REASON} characters"
    return None


def trigger_findings(wfs, cfg):
    jobs_cfg = cfg.get("jobs", {}) if isinstance(cfg.get("jobs"), dict) else {}
    waivers = cfg.get("trigger_waivers", {})
    test_keys, contract_keys = set(cfg.get("test_jobs", [])), set(cfg.get("contract_jobs", []))
    out = []

    def add(code, where, msg, rule=None):
        out.append(f"{code} {where}: {msg} ({rule or RULE[code]})")

    for w in wfs:
        name, jobs, ev = w["name"], w["jobs"], on_events(w)
        keyed = {jid: f"{name}:{jid}" for jid in jobs}
        local = [jid for jid in jobs if isinstance(jobs_cfg.get(keyed[jid]), dict) and "local" in jobs_cfg[keyed[jid]]]
        classed = [jid for jid, job in jobs.items() if jid in local or (isinstance(job, dict) and is_build_job(job))]
        push_main = pushes_main(ev)
        waiver_ok = False
        if name in waivers:
            bad = check_waiver(name, waivers[name])
            if bad:
                add("WAIVER_INVALID", name, bad)
            else:
                waiver_ok = True
        if push_main:
            if waiver_ok:
                if not has_paths(ev["push"]):
                    add("NO_PATHS", name, "waived push to main has no paths:/paths-ignore: filter on the push trigger")
            else:
                for jid in classed:
                    kind = "build" if jid not in local else "test/hygiene"
                    add("PUSH_MAIN", keyed[jid], f"{kind} job runs on push to main with no covering trigger_waivers entry",
                        "rule 3" if jid in local else "rule 5")
            if "pull_request" in ev and not waiver_ok:
                add("PR_AND_PUSH", name, "pull_request and push to main on one workflow; run once, on the PR")
        if "schedule" in ev:
            for jid in local:
                add("SCHEDULE", keyed[jid], "scheduled run of a job with a local parity command")
        if DEPLOY_FILE_RE.match(name.lower()):
            for jid, job in jobs.items():
                if not isinstance(job, dict) or not is_deploy_job(job):
                    continue
                tests = {t.split(":", 1)[1] for t in test_keys if t.split(":", 1)[0] == name}
                if not (has_await(job) or reaches(jobs, jid, tests)):
                    add("DEPLOY_NO_TEST", keyed[jid], "deploys/publishes with no needs: path to a test_jobs entry "
                        "and no await step")
        if any(k.split(":", 1)[0] == name for k in contract_keys):
            filt = [e for e, c in ev.items() if has_paths(c)]
            for k in sorted(contract_keys):
                if k.split(":", 1)[0] == name and filt:
                    add("CONTRACT_PATHS", k, f"contract check on a paths-filtered trigger ({', '.join(sorted(filt))})")
    return out


def cmd_triggers(root, cfg, args):
    errors = validate_triggers_config(cfg)
    if errors:
        raise Fatal("; ".join(errors))
    wfs = load_workflows(root)
    found = trigger_findings(wfs, cfg)
    enforce = cfg.get("triggers", {}).get("enforce", False)
    summary = f"triggers: examined {len(wfs)} workflows, {len(found)} findings"
    if enforce:
        return finish(found, [], summary, examined=len(wfs), what="workflows")
    warns = ["WARN " + f for f in found]
    notes = ["triggers is not enforced for this repo (set \"triggers\": {\"enforce\": true} to fail on findings)"]
    return finish([], warns, summary, notes, examined=1)


# ---------------------------------------------------------------- output / CLI

def finish(findings, warns, summary, notes=(), examined=1, what="uses: lines"):
    for line in findings:
        print(line)
    for line in warns:
        print(line)
    for line in notes:
        print("note: " + line)
    print(summary)
    if not examined:
        print(f"FAIL: examined 0 {what}; a guard that looked at nothing proves nothing")
        return 2
    print(f"{'FAIL' if findings else 'ok'}: {len(findings)} finding(s), {len(warns)} warning(s)")
    return 1 if findings else 0


def default_root():
    p = subprocess.run(["git", "rev-parse", "--show-toplevel"], capture_output=True, text=True)
    return Path(p.stdout.strip()) if p.returncode == 0 and p.stdout.strip() else Path.cwd()


def main(argv=None):
    ap = argparse.ArgumentParser(prog="workflow-preflight", description=__doc__.split("\n")[0])
    ap.add_argument("subcommand", choices=["lint", "parity", "refs", "calls", "dispatch-check", "triggers"])
    ap.add_argument("workflow", nargs="?", help="dispatch-check: the workflow file name")
    ap.add_argument("--root")
    ap.add_argument("--config")
    ap.add_argument("--actionlint")
    ap.add_argument("--shellcheck")
    ap.add_argument("--gate-log")
    ap.add_argument("-R", "--repo", help="refs/dispatch-check: O/R whose names and workflow are read")
    ap.add_argument("--ref", help="dispatch-check: the git ref the workflow is read at")
    ap.add_argument("-f", "--field", action="append", help="dispatch-check: a k=v dispatch input (repeatable)")
    ap.add_argument("--static", action="store_true", help="refs/dispatch-check: no network, syntax and dynamic refs only")
    ap.add_argument("--names-file", help="refs/dispatch-check: JSON names fixture instead of gh (tests)")
    ap.add_argument("--gh", help="refs/dispatch-check: path of the gh binary (default $WORKFLOW_PREFLIGHT_GH or PATH)")
    ap.add_argument("--offline", action="store_true", help="lint: skip the tag/SHA network check (prints a warning)")
    # Intermixed: before Python 3.13, plain parse_known_args fills the optional `workflow`
    # positional with nothing when a flag sits between it and `subcommand`
    # (`dispatch-check -R O/R x.yml`), leaving x.yml as an "unexpected argument".
    args, extra = ap.parse_known_intermixed_args(argv)
    if extra:
        print(f"unexpected arguments: {extra}", file=sys.stderr)
        return 2
    root = Path(args.root).resolve() if args.root else default_root()
    try:
        if args.subcommand == "refs":
            return cmd_refs(root, args)
        if args.subcommand == "dispatch-check":
            return cmd_dispatch_check(root, args)
        if args.subcommand == "calls":
            return cmd_calls(root, args)
        cfg = load_config(Path(args.config) if args.config else root / CONFIG_NAME)
        return {"lint": cmd_lint, "parity": cmd_parity, "triggers": cmd_triggers}[args.subcommand](root, cfg, args)
    except Fatal as e:
        print(f"error: {e}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
