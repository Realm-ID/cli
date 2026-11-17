#!/usr/bin/env python3
# VENDORED from claude-config skills/workflow-preflight @ 16fe65ec19b4 — edit canonical, not this copy
"""Tests for laptop_status.sh and hookaudit.py (CI trigger policy D9).

gh is a stub on PATH: GETs are served from a routes JSON, POSTs to statuses/<sha> are recorded and
then served back by commits/<sha>/status (read-back from the "destination"). Git tests run in real
temp repos with every `git rev-parse --local-env-vars` name scrubbed from the env, so a run from
inside a git hook cannot act on the hooked repo. Temp dirs: $HOOKAUDIT_TEST_TMP or ./.scratch/.
"""
import json, os, shutil, subprocess, tempfile, unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
LAPTOP_STATUS = HERE / "laptop_status.sh"
HOOKAUDIT = HERE / "hookaudit.py"
ZERO = "0" * 40
STUB_FAIL_MARK = "HTTP 401: Bad credentials"

GH_STUB = r'''#!/usr/bin/env python3
import json, os, sys
args = sys.argv[1:]
with open(os.environ["GH_STUB_LOG"], "a") as f:
    f.write(json.dumps(args) + "\n")
if os.environ.get("GH_STUB_EAT_STDIN"):  # behave like a tool that drains whatever stdin it is given
    sys.stdin.read()
if not args or args[0] != "api":
    print("unexpected: %s" % args, file=sys.stderr); sys.exit(9)
method, path, fields, i = "GET", None, {}, 1
while i < len(args):
    a = args[i]
    if a in ("-X", "--method"):
        method = args[i + 1]; i += 2; continue
    if a in ("-f", "-F", "--raw-field", "--field"):
        k, _, v = args[i + 1].partition("="); fields[k] = v; i += 2; continue
    if a in ("--jq", "-q", "-H", "--header"):
        i += 2; continue
    if a.startswith("-"):
        i += 1; continue
    path = a; i += 1
path = path.split("?")[0]
posted_file = os.environ["GH_STUB_POSTED"]
posted = json.load(open(posted_file)) if os.path.exists(posted_file) else []
if method == "GET" and os.environ.get("GH_STUB_AUTH_FAIL") and path.count("/") == 2:
    print("HTTP 401: Bad credentials", file=sys.stderr); sys.exit(1)
if method == "POST":
    if os.environ.get("GH_STUB_POST_FAIL"):
        print("HTTP 502: Bad Gateway", file=sys.stderr); sys.exit(1)
    remote = os.environ.get("GH_STUB_REMOTE")  # GitHub only accepts a status on a commit it has
    if remote:
        import subprocess
        s = path.rsplit("/", 1)[1]
        env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
        if subprocess.run(["git", "--git-dir", remote, "cat-file", "-e", s + "^{commit}"],
                          capture_output=True, env=env).returncode != 0:
            print("No commit found for SHA: %s (HTTP 422)" % s, file=sys.stderr); sys.exit(1)
    posted.append({"path": path, **fields})
    json.dump(posted, open(posted_file, "w"))
    print("{}"); sys.exit(0)
routes = json.load(open(os.environ["GH_STUB_ROUTES"]))
key = "GET " + path
if isinstance(routes.get(key), dict) and "__fail__" in routes[key]:
    print(routes[key]["__fail__"], file=sys.stderr); sys.exit(1)
if path.endswith("/status"):
    sha = path.split("/")[-2]
    body = routes.get(key, {"state": "pending", "statuses": []})
    extra = [{"context": p["context"], "state": p["state"], "description": p.get("description", "")}
             for p in posted if p["path"].endswith("/statuses/" + sha)]
    body = {**body, "statuses": extra[::-1] + body.get("statuses", [])}
    print(json.dumps(body)); sys.exit(0)
if key not in routes:
    print("HTTP 404: Not Found (%s)" % path, file=sys.stderr); sys.exit(1)
r = routes[key]
if isinstance(r, dict) and "__fail__" in r:
    print(r["__fail__"], file=sys.stderr); sys.exit(1)
print(json.dumps(r))
'''


def scratch_base():
    base = Path(os.environ.get("HOOKAUDIT_TEST_TMP") or Path.cwd() / ".scratch" / "hookaudit-tests")
    base.mkdir(parents=True, exist_ok=True)
    return base


def clean_env():
    env = dict(os.environ)
    names = subprocess.run(["git", "rev-parse", "--local-env-vars"], capture_output=True, text=True).stdout.split()
    for n in names:
        env.pop(n, None)
    return env


class Base(unittest.TestCase):
    def setUp(self):
        self.root = Path(tempfile.mkdtemp(prefix="hookaudit-", dir=scratch_base()))
        self.addCleanup(shutil.rmtree, self.root, ignore_errors=True)
        bindir = self.root / "bin"
        bindir.mkdir()
        gh = bindir / "gh"
        gh.write_text(GH_STUB)
        gh.chmod(0o755)
        self.log = self.root / "gh.log"
        self.posted = self.root / "posted.json"
        self.routes_file = self.root / "routes.json"
        self.routes = {}
        self.env = {**clean_env(), "PATH": f"{bindir}:{os.environ['PATH']}", "GH_STUB_LOG": str(self.log),
                    "GH_STUB_POSTED": str(self.posted), "GH_STUB_ROUTES": str(self.routes_file),
                    "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t",
                    "GIT_COMMITTER_EMAIL": "t@t", "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1"}
        self.save_routes()

    def save_routes(self):
        self.routes_file.write_text(json.dumps(self.routes))

    def posts(self):
        return json.loads(self.posted.read_text()) if self.posted.exists() else []

    def git(self, cwd, *args, check=True):
        r = subprocess.run(["git", "-C", str(cwd), *args], capture_output=True, text=True, env=self.env)
        if check and r.returncode != 0:
            raise AssertionError(f"git {args}: {r.stderr}")
        return r.stdout.strip()


# --------------------------------------------------------------------------------- laptop_status.sh

class LaptopStatusTests(Base):
    def setUp(self):
        super().setUp()
        self.remote = self.root / "remote.git"
        self.work = self.root / "work"
        subprocess.run(["git", "init", "-q", "--bare", "-b", "main", str(self.remote)], check=True, env=self.env)
        subprocess.run(["git", "init", "-q", "-b", "main", str(self.work)], check=True, env=self.env)
        self.git(self.work, "remote", "add", "origin", str(self.remote))
        self.commit("base")
        self.git(self.work, "push", "-q", "origin", "main")
        self.base = self.head()
        self.env["LAPTOP_STATUS_REPO"] = "o/r"
        for slug in ("o/r", "Some-Org/some.repo", "O2/r2"):
            self.routes[f"GET repos/{slug}"] = {"full_name": slug, "permissions": {"push": True}}
        self.save_routes()

    def commit(self, name):
        (self.work / f"{name}.txt").write_text(name)
        self.git(self.work, "add", f"{name}.txt")
        self.git(self.work, "commit", "-q", "-m", name)

    def head(self, rev="HEAD"):
        return self.git(self.work, "rev-parse", rev)

    def pending(self):
        p = self.work / ".git" / "laptop-check-pending"
        return p.read_text().split("\n")[:-1] if p.exists() else []

    def run_hook(self, stdin, *args):
        """Phase 1, as git runs .githooks/pre-push."""
        return subprocess.run(["bash", str(LAPTOP_STATUS), *(args or ("origin", str(self.remote)))],
                              input=stdin, capture_output=True, text=True, cwd=self.work, env=self.env)

    def after_push(self, stdin, state="committed"):
        """Phase 2, as git runs .githooks/reference-transaction after updating refs."""
        return subprocess.run(["bash", str(LAPTOP_STATUS), "--after-push", state], input=stdin,
                              capture_output=True, text=True, cwd=self.work, env=self.env)

    def test_pre_push_records_pending_and_posts_nothing(self):
        for n in ("a", "b", "c"):
            self.commit(n)
        tip = self.head()
        r = self.run_hook(f"refs/heads/main {tip} refs/heads/main {self.base}\n")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertEqual(self.posts(), [])  # GitHub does not have these commits yet (HTTP 422)
        want = {self.head("HEAD"), self.head("HEAD~1"), self.head("HEAD~2")}
        self.assertEqual({ln.split()[0] for ln in self.pending()}, want)

    def test_after_push_posts_all_labelled_by_tip(self):
        for n in ("a", "b", "c"):
            self.commit(n)
        tip = self.head()
        self.run_hook(f"refs/heads/main {tip} refs/heads/main {self.base}\n")
        r = self.after_push(f"{self.base} {tip} refs/remotes/origin/main\n")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        posts = self.posts()
        self.assertEqual(len(posts), 3, posts)
        want = {self.head("HEAD"), self.head("HEAD~1"), self.head("HEAD~2")}
        self.assertEqual({p["path"].rsplit("/", 1)[1] for p in posts}, want)
        for p in posts:
            self.assertTrue(p["path"].startswith("repos/o/r/statuses/"), p)
            self.assertEqual(p["state"], "success")
            self.assertEqual(p["context"], "laptop/check")
            self.assertEqual(p["description"], f"checked at tip {tip[:7]}")
        self.assertIn("marked 3 of 3 pending", r.stdout + r.stderr)
        self.assertEqual(self.pending(), [])

    def test_after_push_ignores_other_states_and_local_refs(self):
        self.commit("a")
        tip = self.head()
        self.run_hook(f"refs/heads/main {tip} refs/heads/main {self.base}\n")
        self.after_push(f"{self.base} {tip} refs/remotes/origin/main\n", state="prepared")
        self.after_push(f"{self.base} {tip} refs/heads/main\n")
        self.assertEqual(self.posts(), [])
        self.assertEqual(len(self.pending()), 1)

    def test_after_push_posts_only_ancestors_of_the_updated_ref(self):
        self.commit("a")
        a = self.head()
        self.commit("b")
        b = self.head()
        self.run_hook(f"refs/heads/main {b} refs/heads/main {self.base}\n")
        self.after_push(f"{self.base} {a} refs/remotes/origin/main\n")
        self.assertEqual([p["path"].rsplit("/", 1)[1] for p in self.posts()], [a])
        self.assertEqual([ln.split()[0] for ln in self.pending()], [b])

    def test_after_push_without_pending_is_silent_and_calls_no_gh(self):
        r = self.after_push(f"{ZERO} {self.base} refs/remotes/origin/main\n")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertEqual(r.stdout + r.stderr, "")
        self.assertFalse(self.log.exists() and self.log.read_text().strip())

    def test_late_post_failure_warns_keeps_pending_and_does_not_fail(self):
        self.commit("a")
        tip = self.head()
        self.run_hook(f"refs/heads/main {tip} refs/heads/main {self.base}\n")
        self.env["GH_STUB_POST_FAIL"] = "1"
        r = self.after_push(f"{self.base} {tip} refs/remotes/origin/main\n")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)  # the push already happened
        self.assertIn(f"NOT MARKED {tip}", r.stdout + r.stderr)
        self.assertEqual([ln.split()[0] for ln in self.pending()], [tip])

    def test_every_ref_line_is_read_even_if_gh_drains_stdin(self):
        self.commit("a")
        tip = self.head()
        self.env["GH_STUB_EAT_STDIN"] = "1"
        # pre-push: the gh access check runs on line 1; line 2 must still be read (and is refused)
        r = self.run_hook(f"refs/heads/main {tip} refs/heads/main {self.base}\n"
                          f"refs/heads/old {self.base} refs/heads/old {ZERO}\n")
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertIn("checked tree is not the pushed commit", r.stdout + r.stderr)

    def test_every_after_push_line_is_read_even_if_gh_drains_stdin(self):
        self.commit("a")
        a = self.head()
        self.commit("b")
        b = self.head()
        self.run_hook(f"refs/heads/main {b} refs/heads/main {self.base}\n")
        self.env["GH_STUB_EAT_STDIN"] = "1"
        self.after_push(f"{self.base} {a} refs/remotes/origin/main\n{ZERO} {b} refs/remotes/origin/feat\n")
        self.assertEqual(len(self.posts()), 2, self.posts())

    def test_every_pending_sha_is_read_even_if_gh_drains_stdin(self):
        self.commit("a")
        self.commit("b")
        tip = self.head()
        self.run_hook(f"refs/heads/main {tip} refs/heads/main {self.base}\n")
        self.env["GH_STUB_EAT_STDIN"] = "1"  # in the post loop, stdin would be the pending file
        self.after_push(f"{self.base} {tip} refs/remotes/origin/main\n")
        self.assertEqual(len(self.posts()), 2, self.posts())
        self.assertEqual(self.pending(), [])

    def test_deletion_skipped(self):
        r = self.run_hook(f"(delete) {ZERO} refs/heads/old {self.base}\n")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertEqual(self.pending(), [])

    def test_tag_push_skipped(self):
        self.git(self.work, "tag", "-m", "t", "v1.0.0")
        tag = self.head("v1.0.0")
        r = self.run_hook(f"refs/tags/v1.0.0 {tag} refs/tags/v1.0.0 {ZERO}\n")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertEqual(self.pending(), [])

    def test_pushed_sha_not_head_fails(self):
        self.commit("a")
        self.commit("b")
        older = self.head("HEAD~1")
        r = self.run_hook(f"refs/heads/main {older} refs/heads/main {self.base}\n")
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertIn("checked tree is not the pushed commit", r.stdout + r.stderr)
        self.assertEqual(self.pending(), [])

    def test_dirty_tracked_file_fails(self):
        self.commit("a")
        (self.work / "a.txt").write_text("changed")
        r = self.run_hook(f"refs/heads/main {self.head()} refs/heads/main {self.base}\n")
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertIn("checked tree is not the pushed commit", r.stdout + r.stderr)
        self.assertEqual(self.pending(), [])

    def test_untracked_file_fails(self):
        self.commit("a")
        (self.work / "stray.txt").write_text("x")
        r = self.run_hook(f"refs/heads/main {self.head()} refs/heads/main {self.base}\n")
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertEqual(self.pending(), [])

    def test_untracked_scratch_only_names_the_gitignore_fix(self):
        # F-036: the HTTPS push form skips the global config, so an un-ignored .scratch/ is the dirt
        self.commit("a")
        (self.work / ".scratch").mkdir()
        (self.work / ".scratch" / "out.txt").write_text("x")
        r = self.run_hook(f"refs/heads/main {self.head()} refs/heads/main {self.base}\n")
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertIn("add .scratch/ to this repo's .gitignore", r.stdout + r.stderr)
        self.assertEqual(self.pending(), [])

    def test_other_dirt_beside_scratch_keeps_the_plain_message(self):
        self.commit("a")
        (self.work / ".scratch").mkdir()
        (self.work / ".scratch" / "out.txt").write_text("x")
        (self.work / "stray.txt").write_text("x")
        r = self.run_hook(f"refs/heads/main {self.head()} refs/heads/main {self.base}\n")
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertIn("Check out and push that commit from a clean tree.", r.stdout + r.stderr)
        self.assertNotIn(".gitignore", r.stdout + r.stderr)

    def test_new_branch_records_commits_not_on_remotes(self):
        self.git(self.work, "fetch", "-q", "origin")
        self.git(self.work, "checkout", "-q", "-b", "feat")
        self.commit("a")
        self.commit("b")
        r = self.run_hook(f"refs/heads/feat {self.head()} refs/heads/feat {ZERO}\n")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertEqual(len(self.pending()), 2, self.pending())

    def test_gh_cannot_post_blocks_push(self):
        self.commit("a")
        self.env["GH_STUB_AUTH_FAIL"] = "1"
        r = self.run_hook(f"refs/heads/main {self.head()} refs/heads/main {self.base}\n")
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertIn("gh cannot read o/r", r.stdout + r.stderr)
        self.assertIn("gh auth", r.stdout + r.stderr)
        self.assertEqual(self.pending(), [])

    def test_no_push_permission_blocks_push(self):
        self.commit("a")
        self.routes["GET repos/o/r"] = {"full_name": "o/r", "permissions": {"push": False}}
        self.save_routes()
        r = self.run_hook(f"refs/heads/main {self.head()} refs/heads/main {self.base}\n")
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertEqual(self.pending(), [])

    def test_unknown_remote_sha_fails(self):
        self.commit("a")
        r = self.run_hook(f"refs/heads/main {self.head()} refs/heads/main {'b' * 40}\n")
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertEqual(self.pending(), [])

    def test_slug_from_ssh_and_https_urls(self):
        del self.env["LAPTOP_STATUS_REPO"]
        self.commit("a")
        tip = self.head()
        line = f"refs/heads/main {tip} refs/heads/main {self.base}\n"
        r = self.run_hook(line, "origin", "git@github.com:Some-Org/some.repo.git")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        r = self.run_hook(line, "origin", "https://github.com/O2/r2")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.after_push(f"{self.base} {tip} refs/remotes/origin/main\n")
        paths = sorted(p["path"] for p in self.posts())
        self.assertEqual(len(paths), 2, paths)
        self.assertTrue(paths[0].startswith("repos/O2/r2/statuses/"), paths)
        self.assertTrue(paths[1].startswith("repos/Some-Org/some.repo/statuses/"), paths)

    def test_non_github_url_without_override_fails(self):
        del self.env["LAPTOP_STATUS_REPO"]
        self.commit("a")
        r = self.run_hook(f"refs/heads/main {self.head()} refs/heads/main {self.base}\n")
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertEqual(self.pending(), [])

    def test_push_to_bare_url_warns(self):
        self.commit("a")
        url = "git@github.com:o/r.git"
        r = self.run_hook(f"refs/heads/main {self.head()} refs/heads/main {self.base}\n", url, url)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn("WARNING: pushing to a URL, marks will not be posted", r.stdout + r.stderr)

    def test_recheck_posts_only_that_sha_at_once(self):
        self.commit("a")
        self.commit("b")
        self.env["HOOKAUDIT_RECHECK"] = self.head()
        r = self.run_hook(f"refs/heads/hookaudit-recheck {self.head()} refs/heads/hookaudit-recheck {ZERO}\n")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        posts = self.posts()
        self.assertEqual(len(posts), 1, posts)
        self.assertTrue(posts[0]["path"].endswith(self.head()))
        self.assertEqual(posts[0]["description"], f"rechecked at {self.head()[:7]}")
        self.assertEqual(self.pending(), [])

    def test_recheck_post_failure_fails(self):
        self.commit("a")
        self.env["HOOKAUDIT_RECHECK"] = self.head()
        self.env["GH_STUB_POST_FAIL"] = "1"
        r = self.run_hook(f"refs/heads/hookaudit-recheck {self.head()} refs/heads/hookaudit-recheck {ZERO}\n")
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)


class RealPushTests(Base):
    """Real `git push` through installed hooks; the gh stub refuses (HTTP 422) a status on a commit the
    remote does not have, as GitHub does. This is the test the first design lacked."""

    def setUp(self):
        super().setUp()
        self.remote = self.root / "remote.git"
        self.work = self.root / "work"
        subprocess.run(["git", "init", "-q", "--bare", "-b", "main", str(self.remote)], check=True, env=self.env)
        subprocess.run(["git", "init", "-q", "-b", "main", str(self.work)], check=True, env=self.env)
        self.git(self.work, "remote", "add", "origin", str(self.remote))
        self.commit("base")
        self.git(self.work, "push", "-q", "origin", "main")  # before hooks are installed
        hooks = self.root / "hooks"
        hooks.mkdir()
        (hooks / "pre-push").write_text(f'#!/bin/sh\n[ -f "{self.root}/fail-check" ] && exit 1\n'
                                        f'exec bash "{LAPTOP_STATUS}" "$@"\n')
        (hooks / "reference-transaction").write_text(f'#!/bin/sh\nexec bash "{LAPTOP_STATUS}" --after-push "$@"\n')
        for h in hooks.iterdir():
            h.chmod(0o755)
        self.git(self.work, "config", "core.hooksPath", str(hooks))
        self.env.update(LAPTOP_STATUS_REPO="o/r", GH_STUB_REMOTE=str(self.remote))
        self.routes["GET repos/o/r"] = {"full_name": "o/r", "permissions": {"push": True}}
        self.save_routes()

    commit = LaptopStatusTests.commit
    head = LaptopStatusTests.head
    pending = LaptopStatusTests.pending

    def push(self, *args):
        return subprocess.run(["git", "-C", str(self.work), "push", "origin", *args],
                              capture_output=True, text=True, env=self.env)

    def remote_has(self, s):
        return subprocess.run(["git", "--git-dir", str(self.remote), "cat-file", "-e", s + "^{commit}"],
                              env=self.env).returncode == 0

    def test_push_succeeds_and_marks_every_new_commit(self):
        for n in ("a", "b", "c"):
            self.commit(n)
        r = self.push("main")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertTrue(self.remote_has(self.head()))
        shas = {p["path"].rsplit("/", 1)[1] for p in self.posts()}
        self.assertEqual(shas, {self.head("HEAD"), self.head("HEAD~1"), self.head("HEAD~2")})
        self.assertEqual(self.pending(), [])

    def test_failed_check_pushes_and_marks_nothing(self):
        self.commit("a")
        (self.root / "fail-check").write_text("x")
        r = self.push("main")
        self.assertNotEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertFalse(self.remote_has(self.head()))
        self.assertEqual(self.posts(), [])

    def test_late_failure_is_retried_on_next_push(self):
        self.commit("a")
        first = self.head()
        self.env["GH_STUB_POST_FAIL"] = "1"
        r = self.push("main")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn(f"NOT MARKED {first}", r.stdout + r.stderr)
        del self.env["GH_STUB_POST_FAIL"]
        self.commit("b")
        r = self.push("main")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertEqual({p["path"].rsplit("/", 1)[1] for p in self.posts()}, {first, self.head()})
        self.assertEqual(self.pending(), [])

    def test_fetch_of_others_commits_posts_nothing(self):
        other = self.root / "other"
        subprocess.run(["git", "clone", "-q", str(self.remote), str(other)], check=True, env=self.env)
        (other / "x.txt").write_text("x")
        self.git(other, "add", "x.txt")
        self.git(other, "commit", "-q", "-m", "x")
        self.git(other, "push", "-q", "origin", "main")
        self.git(self.work, "fetch", "-q", "origin")
        self.assertEqual(self.posts(), [])

    def test_rejected_push_marks_nothing_even_after_fetch(self):
        other = self.root / "other"
        subprocess.run(["git", "clone", "-q", str(self.remote), str(other)], check=True, env=self.env)
        (other / "x.txt").write_text("x")
        self.git(other, "add", "x.txt")
        self.git(other, "commit", "-q", "-m", "x")
        self.git(other, "push", "-q", "origin", "main")
        self.commit("a")  # diverges: the push is rejected as non-fast-forward after pre-push ran
        r = self.push("main")
        self.assertNotEqual(r.returncode, 0, r.stdout + r.stderr)
        self.git(self.work, "fetch", "-q", "origin")
        self.assertEqual(self.posts(), [])

    def test_new_branch_push_marks_its_commits(self):
        self.git(self.work, "checkout", "-q", "-b", "feat")
        self.commit("a")
        self.commit("b")
        r = self.push("feat")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertEqual(len(self.posts()), 2, self.posts())



# --------------------------------------------------------------------------------- hookaudit.py

def sha(n):
    return f"{n:02x}" * 20  # distinct in the first 7 chars, as real shas are


S0, C1, C2, C3 = sha(0xa0), sha(0xa1), sha(0xa2), sha(0xa3)
MARK = {"state": "success", "statuses": [{"context": "laptop/check", "state": "success"}]}


def commit_obj(s, parents, login="dev"):
    return {"sha": s, "parents": [{"sha": p} for p in parents], "committer": {"login": login},
            "commit": {"committer": {"name": "GitHub" if login == "web-flow" else "dev"}}}


class HookauditTests(Base):
    """Linear main: S0 (since) <- C1 <- C2 <- C3 (head)."""

    def setUp(self):
        super().setUp()
        self.repo = self.root / "repo"
        self.repo.mkdir()
        self.config({"since": S0})
        r = self.routes
        r["GET repos/o/r/commits/" + S0] = commit_obj(S0, [sha(0x9f)])
        r["GET repos/o/r/commits/" + C1] = commit_obj(C1, [S0])
        r["GET repos/o/r/commits/" + C2] = commit_obj(C2, [C1])
        r["GET repos/o/r/commits/" + C3] = commit_obj(C3, [C2])
        r["GET repos/o/r/commits/main"] = commit_obj(C3, [C2])
        r[f"GET repos/o/r/compare/{S0}...{C3}"] = {"status": "ahead", "ahead_by": 3}
        for s in (C1, C2, C3):
            r[f"GET repos/o/r/commits/{s}/status"] = MARK
        self.save_routes()

    def config(self, obj):
        (self.repo / ".hookaudit.json").write_text(obj if isinstance(obj, str) else json.dumps(obj))

    def unmark(self, s):
        del self.routes[f"GET repos/o/r/commits/{s}/status"]
        self.save_routes()

    def audit(self, *args):
        return subprocess.run(["python3", str(HOOKAUDIT), "--root", str(self.repo), "--repo", "o/r", *args],
                              capture_output=True, text=True, env=self.env)

    def test_all_marked_clean(self):
        r = self.audit()
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn("examined 3 commits, 0 unmarked", r.stdout)

    def test_unmarked_flagged(self):
        self.unmark(C2)
        r = self.audit()
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertIn(C2, r.stdout)
        self.assertIn("no status", r.stdout)
        self.assertIn("examined 3 commits, 1 unmarked", r.stdout)

    def test_failed_or_other_context_is_unmarked(self):
        self.routes[f"GET repos/o/r/commits/{C1}/status"] = {
            "state": "failure", "statuses": [{"context": "laptop/check", "state": "failure"}]}
        self.routes[f"GET repos/o/r/commits/{C2}/status"] = {
            "state": "success", "statuses": [{"context": "ci/build", "state": "success"}]}
        self.save_routes()
        r = self.audit()
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertIn("examined 3 commits, 2 unmarked", r.stdout)

    def github_made(self, pulls):
        for ref in (C3, "main"):  # GitHub serves the same object by sha and by branch name
            self.routes["GET repos/o/r/commits/" + ref] = commit_obj(C3, [C2], login="web-flow")
        self.routes[f"GET repos/o/r/commits/{C3}/pulls"] = pulls
        self.unmark(C3)

    def test_github_made_pr_head_marked_ok(self):
        head = sha(0xbb)
        self.github_made([{"number": 7, "head": {"sha": head}, "merge_commit_sha": C3}])
        self.routes[f"GET repos/o/r/commits/{head}/status"] = MARK
        self.save_routes()
        r = self.audit()
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn("examined 3 commits, 0 unmarked", r.stdout)

    def test_github_made_pr_head_unmarked_flagged(self):
        head = sha(0xbb)
        self.github_made([{"number": 7, "head": {"sha": head}, "merge_commit_sha": C3}])
        r = self.audit()
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertIn(f"GitHub-made, PR #7 head {head[:7]} unmarked", r.stdout)

    def test_github_made_prefers_pr_whose_merge_commit_is_the_sha(self):
        head_other, head_right = sha(0xcc), sha(0xbb)
        self.github_made([{"number": 3, "head": {"sha": head_other}, "merge_commit_sha": sha(0xdd)},
                          {"number": 7, "head": {"sha": head_right}, "merge_commit_sha": C3}])
        self.routes[f"GET repos/o/r/commits/{head_right}/status"] = MARK
        self.save_routes()
        r = self.audit()
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)

    def test_github_made_no_pr_flagged(self):
        self.github_made([])
        r = self.audit()
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertIn("GitHub-made, no PR", r.stdout)

    def test_since_clips_and_is_not_examined(self):
        # S0 has no status route: examining it would read as unmarked, so exit 0 proves it was skipped.
        r = self.audit()
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertNotIn(S0, r.stdout)
        self.assertNotIn(f"commits/{S0}/status", self.log.read_text())

    def assertExit2(self, r, fragment):
        # python3 on a missing script also exits 2: the tool's own message is what proves the verdict
        self.assertEqual(r.returncode, 2, r.stdout + r.stderr)
        self.assertIn("hookaudit:", r.stderr)
        self.assertIn(fragment, r.stderr)

    def test_missing_config_exit_2(self):
        (self.repo / ".hookaudit.json").unlink()
        self.assertExit2(self.audit(), ".hookaudit.json")

    def test_config_without_since_exit_2(self):
        self.config({})
        self.assertExit2(self.audit(), "since")

    def test_unparsable_config_exit_2(self):
        self.config("{not json")
        self.assertExit2(self.audit(), ".hookaudit.json")

    def test_unknown_since_exit_2(self):
        self.config({"since": sha(0xee)})
        self.assertExit2(self.audit(), "since")

    def test_api_error_exit_2(self):
        self.routes[f"GET repos/o/r/commits/{C2}/status"] = {"__fail__": "HTTP 502: Bad Gateway"}
        self.save_routes()
        self.assertExit2(self.audit(), "502")

    def test_diverged_since_exit_2(self):
        self.routes[f"GET repos/o/r/compare/{S0}...{C3}"] = {"status": "diverged", "ahead_by": 3}
        self.save_routes()
        self.assertExit2(self.audit(), "diverged")

    def test_since_not_on_first_parent_chain_exit_2(self):
        # compare says 1 ahead, but walking first parents from C3 does not reach S0 within 1 step
        self.routes[f"GET repos/o/r/compare/{S0}...{C3}"] = {"status": "ahead", "ahead_by": 1}
        self.save_routes()
        self.assertExit2(self.audit(), "first-parent")

    def test_head_equals_since_examines_zero_clean(self):
        self.routes[f"GET repos/o/r/compare/{S0}...{C3}"] = {"status": "identical", "ahead_by": 0}
        self.routes["GET repos/o/r/commits/main"] = commit_obj(S0, [sha(0x9f)])
        self.routes[f"GET repos/o/r/compare/{S0}...{S0}"] = {"status": "identical", "ahead_by": 0}
        self.save_routes()
        r = self.audit()
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn("examined 0 commits, 0 unmarked", r.stdout)

    def test_merge_commit_follows_first_parent_only(self):
        side = sha(0xf0)  # no route: following it would 404 and exit 2
        for ref in (C3, "main"):
            self.routes["GET repos/o/r/commits/" + ref] = commit_obj(C3, [C2, side])
        self.save_routes()
        r = self.audit()
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn("examined 3 commits", r.stdout)

    def tags(self):
        self.routes["GET repos/o/r/git/matching-refs/tags/v"] = [
            {"ref": "refs/tags/v1.0.0"}, {"ref": "refs/tags/v1.1.0"}, {"ref": "refs/tags/v1.10.0-rc1"},
            {"ref": "refs/tags/latest"}]
        self.routes["GET repos/o/r/commits/v1.0.0"] = commit_obj(C1, [S0])
        self.routes["GET repos/o/r/commits/v1.1.0"] = commit_obj(C3, [C2])
        self.save_routes()

    def test_tag_range_previous_tag_to_tag(self):
        self.tags()
        r = self.audit("--tag", "v1.1.0")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn("examined 2 commits, 0 unmarked", r.stdout)  # C3, C2; C1 is v1.0.0
        self.assertNotIn(f"commits/{C1}/status", self.log.read_text())

    def test_tag_range_clipped_at_since(self):
        self.tags()
        self.config({"since": C2})
        self.routes[f"GET repos/o/r/compare/{C2}...{C3}"] = {"status": "ahead", "ahead_by": 1}
        self.save_routes()
        r = self.audit("--tag", "v1.1.0")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn("examined 1 commits, 0 unmarked", r.stdout)

    def test_first_tag_audits_back_to_since(self):
        self.tags()
        self.routes[f"GET repos/o/r/compare/{S0}...{C1}"] = {"status": "ahead", "ahead_by": 1}
        self.save_routes()
        r = self.audit("--tag", "v1.0.0")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn("examined 1 commits, 0 unmarked", r.stdout)

    def test_tag_at_or_before_since_audits_nothing(self):
        self.tags()
        self.config({"since": C3})
        self.routes[f"GET repos/o/r/compare/{C3}...{C1}"] = {"status": "behind", "ahead_by": 0}
        self.save_routes()
        r = self.audit("--tag", "v1.0.0")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn("examined 0 commits, 0 unmarked", r.stdout)

    def test_unknown_tag_exit_2(self):
        self.tags()
        self.assertExit2(self.audit("--tag", "v9.9.9"), "v9.9.9")


class RecheckTests(Base):
    """--recheck runs the repo's own hook in a throwaway worktree; the hook posts via laptop_status.sh."""

    def setUp(self):
        super().setUp()
        self.repo = self.root / "proj" / "repo"
        self.repo.mkdir(parents=True)
        subprocess.run(["git", "init", "-q", "-b", "main", str(self.repo)], check=True, env=self.env)
        (self.repo / ".githooks").mkdir()
        hook = self.repo / ".githooks" / "pre-push"
        hook.write_text(f'#!/bin/sh\n[ -f fail-check ] && {{ echo "check failed"; exit 1; }}\n'
                        f'exec bash "{LAPTOP_STATUS}" "$@"\n')
        hook.chmod(0o755)
        (self.repo / ".hookaudit.json").write_text(json.dumps({"since": ZERO}))
        self.git(self.repo, "add", "-A")
        self.git(self.repo, "commit", "-q", "-m", "init")
        self.git(self.repo, "remote", "add", "origin", "git@github.com:o/r.git")
        self.sha = self.git(self.repo, "rev-parse", "HEAD")
        self.routes["GET repos/o/r"] = {"full_name": "o/r", "permissions": {"push": True}}
        self.save_routes()

    def recheck(self, s):
        return subprocess.run(["python3", str(HOOKAUDIT), "--root", str(self.repo), "--repo", "o/r",
                               "--recheck", s], capture_output=True, text=True, env=self.env)

    def worktrees(self):
        return self.git(self.repo, "worktree", "list").splitlines()

    def test_passing_hook_posts_and_reads_back(self):
        r = self.recheck(self.sha)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        posts = self.posts()
        self.assertEqual(len(posts), 1, posts)
        self.assertTrue(posts[0]["path"].endswith(self.sha))
        self.assertIn("laptop/check", r.stdout)
        self.assertEqual(len(self.worktrees()), 1, self.worktrees())
        self.assertFalse((self.root / "proj" / ".worktrees" / f"hookaudit-{self.sha[:7]}").exists())

    def test_failing_hook_posts_nothing(self):
        (self.repo / "fail-check").write_text("x")
        self.git(self.repo, "add", "fail-check")
        self.git(self.repo, "commit", "-q", "-m", "break")
        bad = self.git(self.repo, "rev-parse", "HEAD")
        r = self.recheck(bad)
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertIn("check failed", r.stdout)            # the hook's own output is relayed
        self.assertIn("hook failed", r.stdout + r.stderr)  # and hookaudit says so
        self.assertEqual(self.posts(), [])
        self.assertEqual(len(self.worktrees()), 1, self.worktrees())

    def test_hook_that_posts_nothing_is_not_a_pass(self):
        hook = self.repo / ".githooks" / "pre-push"
        hook.write_text("#!/bin/sh\nexit 0\n")
        self.git(self.repo, "commit", "-q", "-am", "old hook without the marker line")
        old = self.git(self.repo, "rev-parse", "HEAD")
        r = self.recheck(old)
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertIn("posted no mark", r.stdout + r.stderr)

    def needs_deps(self, setup):
        """A hook that fails without an installed, gitignored deps/ dir (like node_modules)."""
        (self.repo / ".gitignore").write_text("deps/\n")
        hook = self.repo / ".githooks" / "pre-push"
        hook.write_text(f'#!/bin/sh\n[ -d deps ] || {{ echo "deps absent"; exit 1; }}\n'
                        f'exec bash "{LAPTOP_STATUS}" "$@"\n')
        self.git(self.repo, "add", "-A")
        self.git(self.repo, "commit", "-q", "-m", "needs deps")
        cfg = {"since": ZERO}
        if setup is not None:
            cfg["recheck_setup"] = setup
        (self.repo / ".hookaudit.json").write_text(json.dumps(cfg))  # the main checkout's config
        return self.git(self.repo, "rev-parse", "HEAD")

    def test_recheck_setup_runs_in_worktree_before_hook(self):
        s = self.needs_deps("mkdir deps")
        r = self.recheck(s)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertEqual(len(self.posts()), 1, self.posts())
        self.assertFalse((self.repo / "deps").exists())  # ran in the worktree, not the main checkout

    def test_without_setup_a_deps_hook_fails(self):
        s = self.needs_deps(None)
        r = self.recheck(s)
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertIn("deps absent", r.stdout)

    def test_failed_setup_is_exit_1_no_mark_worktree_removed(self):
        s = self.needs_deps("echo boom; exit 3")
        r = self.recheck(s)
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertIn("recheck_setup failed", r.stdout + r.stderr)
        self.assertEqual(self.posts(), [])
        self.assertEqual(len(self.worktrees()), 1, self.worktrees())

    def test_non_string_setup_exit_2(self):
        s = self.needs_deps(["npm", "ci"])
        r = self.recheck(s)
        self.assertEqual(r.returncode, 2, r.stdout + r.stderr)
        self.assertIn("hookaudit: recheck_setup", r.stderr)

    def test_unknown_sha_exit_2(self):
        r = self.recheck("e" * 40)
        self.assertEqual(r.returncode, 2, r.stdout + r.stderr)
        self.assertIn("hookaudit: unknown commit", r.stderr)


if __name__ == "__main__":
    unittest.main()
