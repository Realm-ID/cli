#!/usr/bin/env python3
# VENDORED from claude-config skills/workflow-preflight @ 657f908faaa7 — edit canonical, not this copy
"""hookaudit: list commits on main that never went through the laptop pre-push hook (CI trigger policy D9).

hookaudit [--root R] [--repo O/R] [--branch main | --tag vX.Y.Z | --recheck SHA]

A commit passes when it carries commit status laptop/check=success (posted by laptop_status.sh).
A commit GitHub made (squash/merge of a PR, committer web-flow) passes when that PR's head sha does.
Range: first-parent <branch> after .hookaudit.json "since" (default), or --tag = previous tag..tag,
never at or before "since". --recheck runs the repo's own pre-push hook on SHA in a throwaway worktree;
the hook posts the mark only if its checks pass. No waivers.
Exit 0 clean, 1 unmarked found (or recheck failed), 2 usage / config / API error.
"""
import argparse, json, os, re, subprocess, sys
from pathlib import Path

CONTEXT = "laptop/check"
ZERO = "0" * 40
TAG = re.compile(r"^v(\d+)\.(\d+)\.(\d+)$")


class GhError(Exception):
    pass


def gh(args):
    cmd = [os.environ.get("HOOKAUDIT_GH", "gh"), *args]
    try:
        r = subprocess.run(cmd, capture_output=True, text=True, stdin=subprocess.DEVNULL)
    except OSError as e:
        raise GhError(f"cannot run gh: {e}")
    if r.returncode != 0:
        raise GhError(f"gh {' '.join(args[:2])} failed (rc={r.returncode}): {r.stderr.strip()}")
    try:
        return json.loads(r.stdout)
    except ValueError as e:
        raise GhError(f"unparseable output from gh {' '.join(args[:2])}: {e}")


def api(path):
    return gh(["api", path])


def git_env():
    """Scrub GIT_DIR & co so a run from inside a git hook cannot act on the hooked repo."""
    env = dict(os.environ)
    names = subprocess.run(["git", "rev-parse", "--local-env-vars"], capture_output=True, text=True).stdout.split()
    for n in names:
        env.pop(n, None)
    return env


def git(root, *args, check=True):
    r = subprocess.run(["git", "-C", str(root), *args], capture_output=True, text=True, env=git_env())
    if check and r.returncode != 0:
        raise GhError(f"git {' '.join(args[:2])} failed: {r.stderr.strip()}")
    return r.stdout.strip()


def slug_of(root):
    url = git(root, "remote", "get-url", "origin", check=False)
    m = re.match(r"^(?:git@github\.com:|ssh://git@github\.com/|https://github\.com/)([^/]+/[^/]+?)(?:\.git)?$", url)
    if not m:
        raise GhError(f"cannot read owner/repo from origin '{url}'; pass --repo O/R")
    return m.group(1)


def load_since(root):
    p = Path(root) / ".hookaudit.json"
    if not p.is_file():
        raise GhError(f"no .hookaudit.json at {root} (adoption writes {{\"since\": \"<main sha>\"}})")
    try:
        since = json.loads(p.read_text()).get("since")
    except (ValueError, AttributeError) as e:
        raise GhError(f"unparseable .hookaudit.json: {e}")
    if not isinstance(since, str) or not re.fullmatch(r"[0-9a-f]{7,40}", since):
        raise GhError("since in .hookaudit.json is absent or not a sha")
    return since


def recheck_setup(root):
    """Optional shell command run in the recheck worktree before the hook (e.g. "npm ci")."""
    p = Path(root) / ".hookaudit.json"
    if not p.is_file():
        return None
    try:
        setup = json.loads(p.read_text()).get("recheck_setup")
    except (ValueError, AttributeError) as e:
        raise GhError(f"unparseable .hookaudit.json: {e}")
    if setup is not None and not (isinstance(setup, str) and setup.strip()):
        raise GhError("recheck_setup in .hookaudit.json must be a non-empty shell command string")
    return setup


def marked(base, sha):
    st = api(f"{base}/commits/{sha}/status?per_page=100")
    return any(s.get("context") == CONTEXT and s.get("state") == "success" for s in st.get("statuses") or [])


def verdict(base, commit):
    """None if marked, else the reason it is not."""
    sha = commit["sha"]
    if marked(base, sha):
        return None
    if (commit.get("committer") or {}).get("login") != "web-flow":
        return "no status"
    pulls = api(f"{base}/commits/{sha}/pulls")
    if not pulls:
        return "GitHub-made, no PR"
    pr = next((p for p in pulls if p.get("merge_commit_sha") == sha), pulls[0])
    head = pr["head"]["sha"]
    if marked(base, head):
        return None
    return f"GitHub-made, PR #{pr['number']} head {head[:7]} unmarked"


def previous_tag(base, tag):
    m = TAG.match(tag)
    if not m:
        raise GhError(f"--tag {tag} is not vX.Y.Z")
    want = tuple(map(int, m.groups()))
    names = [r["ref"].rsplit("/", 1)[1] for r in api(f"{base}/git/matching-refs/tags/v")]
    versions = {tuple(map(int, TAG.match(n).groups())): n for n in names if TAG.match(n)}
    if want not in versions:
        raise GhError(f"tag {tag} not found on GitHub")
    older = [v for v in versions if v < want]
    return versions[max(older)] if older else None


def audit(base, since, head_ref, stop_ref):
    """-> (unmarked [(sha, reason)], examined). Walks first parents from head_ref, stopping at since or stop_ref."""
    try:
        since = api(f"{base}/commits/{since}")["sha"]
    except GhError as e:
        raise GhError(f"since {since} is unknown to GitHub: {e}")
    head = api(f"{base}/commits/{head_ref}")
    cmp = api(f"{base}/compare/{since}...{head['sha']}")
    status = cmp.get("status")
    if status in ("identical", "behind"):
        print(f"range empty: {head_ref} is at or before since {since[:7]}")
        return [], 0
    if status != "ahead":
        raise GhError(f"{head_ref} and since {since[:7]} have {status} histories; since must be an ancestor")
    limit = cmp.get("ahead_by") or 0
    stops = {since}
    if stop_ref:
        stops.add(api(f"{base}/commits/{stop_ref}")["sha"])
    unmarked, examined, cur = [], 0, head
    while cur["sha"] not in stops:
        if examined >= limit:
            raise GhError(f"walked {examined} first-parent commits from {head_ref} without reaching "
                          f"since {since[:7]} (compare says {limit} ahead)")
        examined += 1
        reason = verdict(base, cur)
        if reason:
            unmarked.append((cur["sha"], reason))
        parents = cur.get("parents") or []
        if not parents:
            raise GhError(f"history ended at {cur['sha'][:7]} before reaching since {since[:7]}")
        cur = api(f"{base}/commits/{parents[0]['sha']}")
    if limit and not examined and head["sha"] not in stops:
        raise GhError(f"compare says {limit} ahead but 0 commits examined")
    return unmarked, examined


def recheck(root, slug, sha):
    root = Path(root).resolve()
    full = git(root, "rev-parse", "--verify", "--quiet", f"{sha}^{{commit}}", check=False)
    if not full:
        raise GhError(f"unknown commit {sha} in {root} (fetch first)")
    setup = recheck_setup(root)
    wt = root.parent / ".worktrees" / f"hookaudit-{full[:7]}"
    git(root, "worktree", "add", "--detach", str(wt), full)
    try:
        if setup:
            print(f"hookaudit: recheck_setup in {wt}: {setup}")
            s = subprocess.run(["bash", "-c", setup], cwd=wt, env=git_env())
            if s.returncode != 0:
                print(f"hookaudit: recheck_setup failed (rc={s.returncode}); no mark posted")
                return 1
        hook = wt / ".githooks" / "pre-push"
        if not os.access(hook, os.X_OK):
            print(f"hookaudit: {full[:7]} has no executable .githooks/pre-push; cannot recheck", file=sys.stderr)
            return 1
        url = git(root, "remote", "get-url", "origin", check=False)
        env = {**git_env(), "HOOKAUDIT_RECHECK": full, "LAPTOP_STATUS_REPO": slug}
        line = f"refs/heads/hookaudit-recheck {full} refs/heads/hookaudit-recheck {ZERO}\n"
        r = subprocess.run([str(hook), "origin", url], input=line, text=True, cwd=wt, env=env)
        if r.returncode != 0:
            print(f"hookaudit: hook failed on {full[:7]} (rc={r.returncode}); no mark posted")
            return 1
    finally:
        git(root, "worktree", "remove", "--force", str(wt), check=False)
    if not marked(f"repos/{slug}", full):
        print(f"hookaudit: hook passed but posted no mark on {full[:7]} "
              "(its .githooks/pre-push predates the laptop_status.sh line?)")
        return 1
    print(f"rechecked {full[:7]}: {CONTEXT} success now on GitHub")
    return 0


def main(argv=None):
    ap = argparse.ArgumentParser(prog="hookaudit", description=__doc__.splitlines()[0])
    ap.add_argument("--root", default=".")
    ap.add_argument("--repo", dest="slug")
    g = ap.add_mutually_exclusive_group()
    g.add_argument("--branch", default="main")
    g.add_argument("--tag")
    g.add_argument("--recheck", metavar="SHA")
    a = ap.parse_args(argv)
    try:
        slug = a.slug or slug_of(a.root)
        base = f"repos/{slug}"
        if a.recheck:
            return recheck(a.root, slug, a.recheck)
        since = load_since(a.root)
        if a.tag:
            unmarked, examined = audit(base, since, a.tag, previous_tag(base, a.tag))
        else:
            unmarked, examined = audit(base, since, a.branch, None)
    except GhError as e:
        print(f"hookaudit: {e}", file=sys.stderr)
        return 2
    for sha, reason in unmarked:
        print(f"UNMARKED {sha}  {reason}")
    if unmarked:
        print(f"clear one with: hookaudit --recheck <sha> (runs the hook's checks on it; there are no waivers)")
    print(f"examined {examined} commits, {len(unmarked)} unmarked")
    return 1 if unmarked else 0


if __name__ == "__main__":
    sys.exit(main())
