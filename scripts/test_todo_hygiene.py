#!/usr/bin/env python3
# VENDORED from claude-config skills/todo-hygiene @ cf1fd23ff8fe — edit canonical, not this copy
"""Tests for todo_hygiene.py — stdlib only, no runner to install.

Ported 2026-09-24 from auth's `scripts/test_todo_ranking_hygiene.py` (30 cases), then
widened for the fleet version: every fixture now writes a `.todo-hygiene.json`, and the
new cases pin the per-repo options (see SKILL.md for the schema).

HALF 1 — the closure check. Three ways an absent or struck reference can be
read, and getting any two of them confused is a live-item-closing bug:

  A. sub-repo DIRECTORY absent    -> UNRESOLVABLE, exit 0 with a note (CI)
  B. directory present, file gone -> MISSING, exit 1 (a broken reference)
  C. file present, line struck    -> CLOSED, exit 1 (the fault it exists for)

  plus VACUOUS (2026-09-24): live entries, 0 resolved AND 0 unresolvable -> exit 1.
  The guard verified nothing, and it used to say so with rc=0.

HALF 2 — the anchor check. Every reference FORM gets a row, both directions:

  form                        example                     expected
  ANCHOR, target open         `ui/TODO.md:"still open"`   clean, resolved
  ANCHOR, target struck       "                           CLOSED
  ANCHOR, needle gone         `ui/TODO.md:"gone"`         NO_ANCHOR
  ANCHOR, needle not unique   `ui/TODO.md:"item"`         AMBIGUOUS
  LINE                        `ui/TODO.md:4`              FRAGILE
  LNUM                        `ui/TODO.md` L4             FRAGILE
  RANGE                       `ui/TODO.md:3-4`            FRAGILE
  BARE                        `:827`                      UNANCHORED
  PROSE                       line 346                    UNANCHORED
  anything else with `:N`     `tenant-picker:55`          UNCLASSIFIED

HALF 3 — the checkbox check. HALF 4 — line-number references INTO a TODO file
from other docs (Traide's `TODO.md` L2034 shape).

Run:  python3 test_todo_hygiene.py
"""

import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest

# Hermetic under a git hook: a pre-push/pre-commit hook exports GIT_DIR etc.,
# which would make every fixture's `git init` and root lookup target the repo
# being pushed. Tests that need a GIT_* variable set it explicitly.
for _k in [k for k in os.environ if k.startswith('GIT_')]:
    del os.environ[_k]

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, 'todo_hygiene.py')

# auth's convention: a numbered "Next up" ranking, `~~` closes, `[x]` banned.
AUTH_CFG = {
    'todo': 'TODO.md',
    'ranking': {'heading': 'Next up', 'retired': 'strike', 'entry': 'numbered'},
    'closed_marker': 'strike',
    'checked_boxes': {'forbid': True, 'scope': 'any',
                      'remedy': 'convert it to a struck blockquote record '
                                '(`> ~~**old head**~~ **CLOSED <date>** — …`); '
                                'never tick, never delete'},
    'line_refs_into_todo_from': [],
    'absent_target': 'note',
    'absent_dirs': ['ui'],
    'fragile_line_refs': 'fail',
    'todo_names': ['TODO.md'],
    'skip_dirs': [],
}

# A minimal root TODO.md, built around ONE entry body supplied per test.
# Deliberately NOT a copy of a real file — the thing under test is the
# resolution logic, not today's contents.
#
# The retired section is always present: its entry points at a line that the
# CLOSED fixture strikes through, so every test that expects a clean run is
# also asserting that the retired ranking was skipped.
ROOT = """# TODO — fixture

## ~~Next up (re-ranked 2026-01-01)~~ — RETIRED, must be ignored

1. **A retired entry that must not be checked** (`ui/TODO.md:4`).

## Next up (re-ranked 2026-02-02)

1. **The entry under test.**
%s

## Some other section
"""

SUB_OPEN = """# TODO — ui

- [ ] the first item
- [ ] **the item the root ranking points at** — still open
"""

SUB_CLOSED = """# TODO — ui

- [ ] the first item
- [ ] ~~**the item the root ranking points at**~~ — still open
"""


class Case(unittest.TestCase):
    def setUp(self):
        self.tree = tempfile.mkdtemp(prefix='todo-hygiene-')
        self.addCleanup(shutil.rmtree, self.tree, ignore_errors=True)
        self.cfg = json.loads(json.dumps(AUTH_CFG))

    def write(self, rel, text):
        full = os.path.join(self.tree, rel)
        os.makedirs(os.path.dirname(full), exist_ok=True)
        with open(full, 'w') as f:
            f.write(text)

    def root(self, body):
        self.write('TODO.md', ROOT % body)

    def sub(self, text=SUB_OPEN, name='TODO.md'):
        self.write(os.path.join('ui', name), text)

    def run_guard(self, config=True, where=('scripts',), args=None):
        """`--root` is passed EXPLICITLY unless a test is about root discovery:
        the tempdir may sit inside a git repo (TMPDIR=$PWD/.scratch), and then
        git's toplevel would be that repo, not the fixture (critic, 2026-09-24)."""
        if config:
            self.write('.todo-hygiene.json', json.dumps(self.cfg))
        dst = os.path.join(self.tree, *where, 'todo_hygiene.py')
        os.makedirs(os.path.dirname(dst), exist_ok=True)
        shutil.copy(SCRIPT, dst)
        if args is None:
            args = ['--root', self.tree]
        p = subprocess.run([sys.executable, dst, *args], cwd=self.tree,
                           capture_output=True, text=True)
        return p.returncode, p.stdout + p.stderr

    def check(self, body, rc, *must_contain, **kw):
        """Run one row of the table and assert its outcome."""
        self.root(body)
        if kw.get('sub', True):
            self.sub(kw.get('text', SUB_OPEN))
        code, out = self.run_guard()
        self.assertEqual(code, rc, out)
        for s in must_contain:
            self.assertIn(s, out)
        for s in kw.get('must_not', ()):
            self.assertNotIn(s, out)
        return out

    def run_todo(self, todo, rc, *must_contain, must_not=()):
        """Run against a whole TODO.md supplied verbatim."""
        self.write('TODO.md', todo)
        code, out = self.run_guard()
        self.assertEqual(code, rc, out)
        for s in must_contain:
            self.assertIn(s, out)
        for s in must_not:
            self.assertNotIn(s, out)
        return out

    # ================= HALF 1: the closure check ============================
    def test_absent_sub_repo_is_unresolvable_not_closed(self):
        """No `ui/` at all — CI's checkout. Must NOT fail, must NOT say closed.
        Regression guard for auth runs 33042469617 / 33043412926 / 33043759045."""
        out = self.check('   Ranks `ui/TODO.md:"still open"`.', 0,
                         'unresolvable', 'not in this checkout', 'NOT a closure',
                         sub=False, must_not=('❌', 'already closed', 'CLOSED'))
        self.assertIn('resolved no references at all', out)

    def test_present_sub_repo_with_missing_file_still_fails(self):
        os.makedirs(os.path.join(self.tree, 'ui'), exist_ok=True)
        self.check('   Ranks `ui/TODO.md:"still open"`.', 1, 'MISSING',
                   sub=False)

    def test_anchor_on_a_struck_target_is_closed(self):
        self.check('   Ranks `ui/TODO.md:"still open"`.', 1, 'CLOSED',
                   text=SUB_CLOSED)

    def test_line_on_a_struck_target_is_fragile_not_closed(self):
        """A LINE reference NEVER reports CLOSED — only an ANCHOR may."""
        self.check('   Ranks `ui/TODO.md:4`.', 1, 'FRAGILE', text=SUB_CLOSED,
                   must_not=('CLOSED',))

    def test_line_drifted_onto_an_unrelated_struck_line_is_not_falsely_closed(self):
        drifted = ('# TODO — ui\n\n'
                   '- [ ] the first item\n'
                   '- [ ] ~~an unrelated, already-closed item~~\n'
                   '- [ ] **the item the root ranking points at** — still open\n')
        out = self.check('   Ranks `ui/TODO.md:4`.', 1, 'FRAGILE',
                         'not proof this is the same item', text=drifted,
                         must_not=('CLOSED',))
        self.assertIn('unrelated, already-closed item', out)

    def test_retired_ranking_is_not_checked(self):
        out = self.check('   Ranks `ui/TODO.md:"still open"`.', 1, 'CLOSED',
                         text=SUB_CLOSED)
        self.assertEqual(out.count('- ❌'), 1, out)

    def test_struck_reference_inside_a_live_entry_is_ignored(self):
        self.check('   Used to say ~~`ui/TODO.md:200`~~, retracted.\n'
                   '   Ranks `ui/TODO.md:"still open"`.', 0,
                   '1 reference(s) resolved', must_not=('❌',))

    # --- VACUOUS (auth-50's defect, 2026-09-24) ---
    def test_ranking_that_resolves_nothing_and_cannot_blame_ci_is_vacuous(self):
        """auth's live state on 2026-09-24: 6 entries, 0 resolved, 0 unresolvable,
        rc=0. Nothing was verified and nothing was out of reach — that is a
        ranking whose entries carry no references at all, and it must fail."""
        self.check('   Just prose, no reference to any item.', 1, 'VACUOUS')

    def test_zero_resolved_but_some_unresolvable_still_passes_with_warning(self):
        """The CI branch of the same count: the refs exist, the sub-repos don't.
        auth-50's explicit constraint — this must NOT become VACUOUS."""
        self.check('   Ranks `ui/TODO.md:"still open"`.', 0,
                   'resolved no references at all', sub=False,
                   must_not=('VACUOUS',))

    # ================= HALF 2: the anchor check =============================
    def test_anchor_on_an_open_target_passes(self):
        self.check('   Ranks `ui/TODO.md:"still open"`.', 0,
                   '1 reference(s) resolved', 'anchored to text',
                   must_not=('❌',))

    def test_anchor_is_backtick_and_whitespace_insensitive(self):
        self.sub('# TODO\n\n- [ ] **Delete the `renameScope`   shim**\n'
                 '- [ ] a fourth line, so `ui/TODO.md:4` resolves\n')
        self.check('   Ranks `ui/TODO.md:"Delete the renameScope shim"`.', 0,
                   '1 reference(s) resolved', sub=False)

    def test_anchor_whose_needle_is_gone_fails_loudly(self):
        self.check('   Ranks `ui/TODO.md:"a needle nobody wrote"`.', 1,
                   'NO_ANCHOR')

    def test_anchor_that_matches_two_lines_is_ambiguous(self):
        self.check('   Ranks `ui/TODO.md:"item"`.', 1, 'AMBIGUOUS',
                   'lengthen the needle')

    def test_line_reference_is_fragile_even_when_it_resolves(self):
        self.check('   Ranks `ui/TODO.md:4`.', 1, 'FRAGILE',
                   'rots on the next edit above it')

    def test_lnum_reference_is_fragile(self):
        """Traide's live stale-pointer shape: `TODO.md` L2034."""
        self.check('   Ranks `ui/TODO.md` L4.', 1, 'FRAGILE',
                   must_not=('UNCLASSIFIED',))

    def test_line_reference_past_the_end_of_the_file_is_drifted(self):
        self.check('   Ranks `ui/TODO.md:999`.', 1, 'DRIFTED')

    def test_line_range_is_fragile(self):
        self.check('   Ranks `ui/TODO.md:3-4`.', 1, 'FRAGILE', 'line RANGE')

    def test_anchor_ignores_a_self_match_in_the_citing_line(self):
        self.check('   Ranks `TODO.md:"a needle nobody else wrote"`.', 1,
                   'NO_ANCHOR', sub=False, must_not=('1 reference(s) resolved',))

    def test_bare_line_number_is_unanchored(self):
        self.check('   Ranks the shim (`:827`).', 1, 'UNANCHORED')

    def test_prose_line_number_is_unanchored(self):
        self.check('   The script flagged line 346 of that file.', 1,
                   'UNANCHORED')
        self.check('   It had drifted to lines 566 of the same file.', 1,
                   'UNANCHORED')

    def test_whole_file_sweep_catches_an_out_of_range_line_ref_outside_the_ranking(self):
        self.sub()
        self.run_todo('# TODO — fixture\n\n## Next up (re-ranked 2026-02-02)\n\n'
                      '1. **The entry under test.**\n'
                      '   Ranks `ui/TODO.md:"still open"`.\n\n'
                      '## Some other section\n\n'
                      'See `ui/TODO.md:999` for the old note.\n', 1,
                      'DRIFTED', 'ui/TODO.md:999', 'out-of-range')

    def test_trailing_newline_does_not_inflate_the_line_count(self):
        self.sub()
        self.write('ui/three.md', 'one\ntwo\nthree\n')
        self.run_todo('# TODO — fixture\n\n## Next up (re-ranked 2026-02-02)\n\n'
                      '1. **The entry under test.**\n'
                      '   Ranks `ui/TODO.md:"still open"`.\n\n'
                      '## Some other section\n\n'
                      'See `ui/three.md:4` for the old note.\n', 1,
                      'DRIFTED', 'ui/three.md:4', 'has 3 lines')

    def test_whole_file_sweep_counts_but_does_not_verify_in_range_refs(self):
        self.sub()
        self.run_todo('# TODO — fixture\n\n## Next up (re-ranked 2026-02-02)\n\n'
                      '1. **The entry under test.**\n'
                      '   Ranks `ui/TODO.md:"still open"`.\n\n'
                      '## Some other section\n\n'
                      'See `ui/TODO.md:2` for the old note.\n', 0,
                      '1 in-range line reference(s)', must_not=('DRIFTED',))

    def test_a_checked_box_is_not_a_closure_marker(self):
        """Under closed_marker=strike, `- [x]` is an anomaly (HALF 3), not a
        closure (HALF 1). The needle is the BOLDED `**CLOSED**` the finding line
        prints; the remedy text itself spells `**CLOSED <date>**`."""
        self.sub('# TODO\n\n- [ ] first\n- [x] **DONE — the item ranked here**\n')
        out = self.check('   Ranks `ui/TODO.md:"the item ranked here"`.', 1,
                         '1 reference(s) resolved', 'CHECKBOX',
                         sub=False, must_not=('**CLOSED**',))
        self.assertIn('ui/TODO.md:4', out)

    # ================= HALF 3: the checkbox check ===========================
    def test_a_checked_box_in_the_root_file_is_a_finding(self):
        self.root('   Ranks `ui/TODO.md:"still open"`.\n\n- [x] **A ticked box**')
        self.sub()
        code, out = self.run_guard()
        self.assertEqual(code, 1, out)
        self.assertIn('CHECKBOX', out)
        self.assertIn('convert it to a struck blockquote', out.lower())

    def test_a_checked_box_in_a_sub_repo_todo_is_a_finding(self):
        self.sub('# TODO\n\n- [ ] open\n- [x] **DONE 2026-08-26 — something**\n')
        out = self.check('   Ranks `ui/TODO.md:"open"`.', 1, 'CHECKBOX',
                         sub=False)
        self.assertIn('ui/TODO.md:4', out)

    def test_a_checked_box_inside_a_code_fence_is_not_a_finding(self):
        self.sub('# TODO\n\n- [ ] open\n\n```\n- [x] never write this\n```\n')
        self.check('   Ranks `ui/TODO.md:"open"`.', 0, must_not=('CHECKBOX',),
                   sub=False)

    def test_a_quoted_checkbox_in_a_closure_record_is_not_a_finding(self):
        self.sub('# TODO\n\n- [ ] open\n> ~~**old head**~~ was `- [x]`\n'
                 '> - [x] **DONE** — the record as it used to read\n')
        self.check('   Ranks `ui/TODO.md:"open"`.', 0, must_not=('CHECKBOX',),
                   sub=False)

    def test_the_checkbox_scan_names_the_files_it_could_see(self):
        out = self.check('   Ranks `ui/TODO.md:"still open"`.', 0,
                         'Checkbox scan', '`TODO.md`', '`ui/TODO.md`',
                         '0 `- [x]` record(s)')
        self.assertIn('2 TODO file(s)', out)

    def test_top_level_scope_allows_indented_sub_items(self):
        """Traide: a `- [x]` sub-item under an OPEN parent is progress, not a
        leaked closed record."""
        self.cfg['checked_boxes']['scope'] = 'top-level'
        self.sub()
        self.run_todo(ROOT % '   Ranks `ui/TODO.md:"still open"`.'
                      + '\n- [ ] parent\n  - [x] a finished sub-step\n', 0,
                      must_not=('CHECKBOX',))

    def test_top_level_scope_still_flags_a_top_level_tick(self):
        self.cfg['checked_boxes']['scope'] = 'top-level'
        self.sub()
        self.run_todo(ROOT % '   Ranks `ui/TODO.md:"still open"`.'
                      + '\n- [x] a closed record left in place\n', 1, 'CHECKBOX')

    def test_ticks_under_a_closed_section_are_allowed(self):
        """AutoMahn: `[x]` items belong under `## Recently closed`."""
        self.cfg['closed_sections'] = ['Recently closed']
        self.sub()
        self.run_todo(ROOT % '   Ranks `ui/TODO.md:"still open"`.'
                      + '\n## Recently closed\n\n- [x] done thing\n', 0,
                      must_not=('CHECKBOX',))

    def test_checkbox_remedy_comes_from_config(self):
        self.cfg['checked_boxes']['remedy'] = 'move it to TODO-ARCHIVE.md'
        self.sub()
        self.run_todo(ROOT % '   Ranks `ui/TODO.md:"still open"`.'
                      + '\n- [x] leaked\n', 1, 'move it to TODO-ARCHIVE.md')

    def test_struck_open_box_is_flagged_when_enabled(self):
        """AutoMahn: `- [ ] ~~…~~ ALREADY FIXED` is done work nobody ticked."""
        self.cfg.update(closed_marker='checkbox', flag_struck_open=True)
        self.cfg['checked_boxes']['forbid'] = False
        self.cfg['ranking'] = None
        self.run_todo('# TODO\n\n- [ ] open\n- [ ] ~~old thing~~ ALREADY FIXED\n',
                      1, 'STRUCK_OPEN', 'TODO.md:4')

    # ================= config-driven closure and ranking ====================
    def test_no_ranking_configured_skips_half_one(self):
        self.cfg['ranking'] = None
        self.run_todo('# TODO\n\n- [ ] 🔴 something\n', 0,
                      'no ranking configured', must_not=('VACUOUS',))

    def test_checkbox_closed_marker_reads_a_tick_as_closed(self):
        self.cfg['closed_marker'] = 'checkbox'
        self.cfg['checked_boxes']['forbid'] = False
        self.sub('# TODO\n\n- [ ] first\n- [x] **the item ranked here**\n')
        self.check('   Ranks `ui/TODO.md:"the item ranked here"`.', 1,
                   '**CLOSED**', sub=False)

    def test_a_tick_emoji_is_not_closure(self):
        """Traide: ✅ inside an OPEN item marks a finished sub-part, not the item."""
        self.cfg['closed_marker'] = 'checkbox'
        self.sub('# TODO\n\n- [ ] first\n- [ ] **the item ranked here** ✅ part 1\n')
        self.check('   Ranks `ui/TODO.md:"the item ranked here"`.', 0,
                   sub=False, must_not=('CLOSED',))

    def test_closed_pattern_marks_closure(self):
        """AutoMahn: "ANSWERED <date>" closes a question in place."""
        self.cfg['closed_marker'] = 'checkbox'
        self.cfg['closed_patterns'] = [r'ANSWERED \d{4}-\d{2}-\d{2}']
        self.sub('# TODO\n\n- [ ] first\n- [ ] **the item ranked here** '
                 'ANSWERED 2026-09-01\n')
        self.check('   Ranks `ui/TODO.md:"the item ranked here"`.', 1,
                   '**CLOSED**', sub=False)

    def test_bullet_ranking_entries(self):
        self.cfg['ranking'] = {'heading': 'Priority', 'retired': 'strike',
                               'entry': 'bullet'}
        self.sub()
        self.run_todo('# TODO\n\n## ⭐ Priority\n\n'
                      '- [ ] **x** ranks `ui/TODO.md:"still open"`.\n\n## Other\n',
                      0, '1 reference(s) resolved')

    def test_opt_out_region_is_not_scanned(self):
        """Traide's header prose discusses `[x]` and line numbers."""
        self.sub()
        self.run_todo(ROOT % '   Ranks `ui/TODO.md:"still open"`.'
                      + '\n<!-- todo-hygiene: off -->\n- [x] example\n'
                      'See `ui/TODO.md:999`.\n<!-- todo-hygiene: on -->\n', 0,
                      must_not=('CHECKBOX', 'DRIFTED'))

    def test_refs_inside_a_closed_entry_are_not_swept(self):
        """AutoMahn: a closed record's `file:line` is history, not a live claim."""
        self.cfg['closed_sections'] = ['Recently closed']
        self.sub()
        self.run_todo(ROOT % '   Ranks `ui/TODO.md:"still open"`.'
                      + '\n## Recently closed\n\n- [x] done\n'
                      '  was at `ui/TODO.md:999`\n', 0, must_not=('DRIFTED',))

    def test_absent_target_fail_mode(self):
        """A single repo has no nested sub-repos: an absent target is rot."""
        self.cfg['absent_target'] = 'fail'
        self.check('   Ranks `ui/TODO.md:"still open"`.', 1, 'MISSING', sub=False)

    # ================= HALF 4: reverse refs into TODO files ================
    def _half4(self, doc_text, fragile='fail'):
        self.cfg['ranking'] = None
        self.cfg['line_refs_into_todo_from'] = ['CLAUDE.md', 'plans/*.md']
        self.cfg['fragile_line_refs'] = fragile
        self.write('CLAUDE.md', doc_text)
        self.write('TODO.md', '# TODO\n\n- [ ] one\n- [ ] two\n')
        return self.run_guard()

    def test_half4_line_ref_into_todo_is_fragile(self):
        rc, out = self._half4('See `TODO.md` L3 for the plan.\n')
        self.assertEqual(rc, 1, out)
        self.assertIn('FRAGILE', out)
        self.assertIn('CLAUDE.md:1', out)

    def test_half4_warn_mode_does_not_fail(self):
        rc, out = self._half4('See `TODO.md:3`.\n', fragile='warn')
        self.assertEqual(rc, 0, out)
        self.assertIn('FRAGILE', out)
        self.assertIn('⚠️', out)

    def test_half4_date_is_not_a_line_ref(self):
        rc, out = self._half4('Per `DECISIONS.md` 2026-08-30 and TODO.md 2026-08-30.\n')
        self.assertEqual(rc, 0, out)
        self.assertNotIn('FRAGILE', out)

    def test_half4_glob_is_expanded(self):
        rc, out = self._half4('nothing\n')
        self.write('plans/p.md', 'Picks up `TODO.md:4`.\n')
        rc, out = self.run_guard()
        self.assertEqual(rc, 1, out)
        self.assertIn('plans/p.md:1', out)

    def test_half4_absent_sibling_is_a_note(self):
        rc, out = self._half4('See `ui/TODO.md:4`.\n')
        self.assertEqual(rc, 0, out)
        self.assertIn('not in this checkout', out)

    # ================= refusing to swallow, and refusing to over-report =====
    def test_unrecognised_shape_is_reported_not_skipped(self):
        self.check('   The flake at `tenant-picker:55`.', 1, 'UNCLASSIFIED',
                   'no row in FORMS matches this shape')

    def test_a_timestamp_is_not_a_line_reference(self):
        self.check('   Latest run `32927914029` at `2026-08-26T03:49Z`, and it\n'
                   '   ranks `ui/TODO.md:"still open"`.', 0,
                   must_not=('❌', 'UNCLASSIFIED'))

    def test_half_two_still_runs_when_the_sub_repo_is_absent(self):
        self.check('   Ranks `ui/TODO.md:4`.', 1, 'FRAGILE',
                   'not in this checkout', sub=False)

    def test_non_standard_todo_name_is_listed_as_not_scanned(self):
        self.sub()
        self.sub('# archive\n- [x] old\n', name='TODO-ARCHIVE.md')
        self.root('   Ranks `ui/TODO.md:"still open"`.')
        rc, out = self.run_guard()
        self.assertEqual(rc, 0, out)
        self.assertIn('not scanned', out)
        self.assertIn('ui/TODO-ARCHIVE.md', out)

    # ================= refusing to report nothing as fine ===================
    def test_missing_config_is_a_usage_error(self):
        self.root('   Ranks `ui/TODO.md:"still open"`.')
        rc, out = self.run_guard(config=False)
        self.assertEqual(rc, 2, out)
        self.assertIn('.todo-hygiene.json', out)

    def test_unknown_config_key_is_a_usage_error(self):
        self.cfg['closed_markr'] = 'strike'
        self.root('   Ranks `ui/TODO.md:"still open"`.')
        rc, out = self.run_guard()
        self.assertEqual(rc, 2, out)

    def test_no_ranking_at_all_is_a_parse_error_not_a_pass(self):
        self.write('TODO.md', '# TODO\n\n## Nothing that looks like a ranking\n')
        rc, out = self.run_guard()
        self.assertEqual(rc, 2, out)
        self.assertIn('broken derivation', out)

    def test_ranking_with_no_entries_is_a_parse_error(self):
        self.write('TODO.md', '# TODO\n\n## Next up (re-ranked 2026-02-02)\n\nprose only\n')
        rc, out = self.run_guard()
        self.assertEqual(rc, 2, out)
        self.assertIn('contains no entries', out)

    def test_root_comes_from_git_when_script_is_deeper(self):
        """A vendored copy may sit at any depth; the repo root is git's answer."""
        subprocess.run(['git', 'init', '-q', self.tree], check=True)
        self.sub()
        self.root('   Ranks `ui/TODO.md:"still open"`.')
        rc, out = self.run_guard(where=('tools', 'hygiene'), args=[])
        self.assertEqual(rc, 0, out)
        self.assertIn('1 reference(s) resolved', out)

    # ================= critic round 1 (2026-09-24) ==========================
    def test_fenced_example_of_the_off_marker_does_not_disable_the_rest(self):
        """A rules header documents the marker inside a fence. It must not open
        a region — before the fix everything below it was silently blanked."""
        self.sub()
        self.run_todo(ROOT % '   Ranks `ui/TODO.md:"still open"`.'
                      + '\n```\n<!-- todo-hygiene: off -->\n```\n- [x] leaked\n',
                      1, 'CHECKBOX')

    def test_opt_out_region_closes_and_scanning_resumes(self):
        self.sub()
        self.run_todo(ROOT % '   Ranks `ui/TODO.md:"still open"`.'
                      + '\n<!-- todo-hygiene: off -->\n- [x] example\n'
                      '<!-- todo-hygiene: on -->\n- [x] real leak\n', 1,
                      'real leak', must_not=('example',))

    def test_unclosed_opt_out_region_is_an_error(self):
        self.sub()
        self.run_todo(ROOT % '   Ranks `ui/TODO.md:"still open"`.'
                      + '\n<!-- todo-hygiene: off -->\n- [x] hidden\n', 2,
                      'never closed')

    def test_ref_under_a_closed_item_outside_any_closed_section_is_not_swept(self):
        """closed_item_lines alone, no closed section to mask a mutant."""
        self.cfg['checked_boxes']['forbid'] = False
        self.sub()
        self.run_todo(ROOT % '   Ranks `ui/TODO.md:"still open"`.'
                      + '\n- [x] done\n  was at `ui/TODO.md:999`\n', 0,
                      must_not=('DRIFTED',))

    def test_ref_in_a_struck_blockquote_record_body_is_not_swept(self):
        """strike mode: `> ~~old~~ CLOSED` then `> verified at …:999`."""
        self.sub()
        self.run_todo(ROOT % '   Ranks `ui/TODO.md:"still open"`.'
                      + '\n> ~~**old**~~ **CLOSED 2026-01-01**\n'
                      '> verified at `ui/TODO.md:999`\n', 0,
                      must_not=('DRIFTED',))

    def test_lnum_outside_the_ranking_is_swept_for_drift(self):
        self.sub()
        self.run_todo(ROOT % '   Ranks `ui/TODO.md:"still open"`.'
                      + '\nOld note: `ui/TODO.md` L999.\n', 1, 'DRIFTED')

    def test_indented_struck_open_box_is_flagged_under_scope_any(self):
        self.cfg.update(closed_marker='checkbox', flag_struck_open=True, ranking=None)
        self.cfg['checked_boxes']['forbid'] = False
        self.run_todo('# TODO\n\n- [ ] parent\n  - [ ] ~~sub~~ ALREADY FIXED\n',
                      1, 'STRUCK_OPEN')

    def test_vacuous_is_judged_per_entry(self):
        """One anchored entry must not cover for an entry with no reference."""
        self.sub()
        self.run_todo('# TODO\n\n## Next up\n\n1. **a** `ui/TODO.md:"still open"`\n'
                      '2. **b** no reference at all\n\n## Other\n', 1,
                      'VACUOUS', 'entry 2')

    # ================= feedback round: Traide (2026-09-24) ==================
    def _nested_ui_tick(self):
        self.sub('# TODO\n\n- [ ] **the item the root ranking points at**\n'
                 '- [x] leaked in a nested repo\n')
        os.makedirs(os.path.join(self.tree, 'ui', '.git'))
        self.root('   Ranks `ui/TODO.md:"the item the root ranking points at"`.')

    def test_nested_repo_is_skipped_by_default_and_listed(self):
        """A subdir with its own .git is another repo's backlog: the parent's
        gate must not fail on it, and CI (which lacks it) must not disagree
        with the laptop. Skipped — and SAID so, never silently."""
        self._nested_ui_tick()
        rc, out = self.run_guard()
        self.assertEqual(rc, 0, out)
        self.assertIn('Nested repo(s) not scanned', out)
        self.assertIn('`ui/`', out)
        self.assertIn('1 reference(s) resolved', out)  # anchors INTO it still resolve

    def test_nested_repo_scan_mode_walks_into_it(self):
        """auth scans its nested sub-repos on purpose (stronger locally than CI)."""
        self.cfg['nested_repos'] = 'scan'
        self._nested_ui_tick()
        rc, out = self.run_guard()
        self.assertEqual(rc, 1, out)
        self.assertIn('CHECKBOX', out)

    def _history(self, cutoff):
        self.cfg['dated_history_before'] = cutoff
        return self._half4('## 2026-01-01 — old entry\n\nIt sat at `TODO.md` L3.\n\n'
                           '## 2026-12-01 — new entry\n\nSee `TODO.md` L4.\n')

    def test_dated_history_before_cutoff_is_exempt(self):
        """A dated log entry records where an item sat THAT day; logs are not
        rewritten. Entries on or after the cutoff are still checked."""
        rc, out = self._history('2026-06-01')
        self.assertEqual(rc, 1, out)
        self.assertEqual(out.count('**FRAGILE**'), 1, out)
        self.assertIn('CLAUDE.md:7', out)
        self.assertIn('1 skipped as dated history', out)

    def test_dated_history_cutoff_is_validated(self):
        self.cfg['dated_history_before'] = 'last tuesday'
        self.root('   Ranks `ui/TODO.md:"still open"`.')
        rc, out = self.run_guard()
        self.assertEqual(rc, 2, out)
        self.assertIn('YYYY-MM-DD', out)

    def test_self_contained_entry_is_declared_not_vacuous(self):
        """An entry that IS the item (no sub-repo item to point at) says so
        explicitly; silence is still VACUOUS."""
        self.sub()
        self.run_todo('# TODO\n\n## Next up\n\n1. **a** `ui/TODO.md:"still open"`\n'
                      '2. **b** described here <!-- todo-hygiene: self-contained -->\n'
                      '\n## Other\n', 0, '1 self-contained', must_not=('VACUOUS',))

    def test_absent_dir_not_listed_is_missing_not_a_note(self):
        """A typo'd directory is not a nested repo — it must not pass as one."""
        self.check('   Ranks `uii/TODO.md:"still open"`.', 1, 'MISSING')

    def test_dot_slash_self_anchor_still_excludes_the_citing_entry(self):
        self.check('   Ranks `./TODO.md:"a needle nobody else wrote"`.', 1,
                   'NO_ANCHOR', sub=False)

    def test_half4_mask_and_closed_items_apply(self):
        rc, out = self._half4('```\nSee `TODO.md:3`.\n```\n- [x] done\n'
                              '  was `TODO.md:4`\n')
        self.assertEqual(rc, 0, out)

    def test_half4_ignores_files_that_merely_contain_todo(self):
        rc, out = self._half4('See `docs/autodoc.md:12`, `mastodon.md:3`, '
                              'photodocs.md L4.\n')
        self.assertEqual(rc, 0, out)

    def test_half4_github_link_and_prose_forms(self):
        rc, out = self._half4('See TODO.md#L3 and TODO.md line 4.\n')
        self.assertEqual(rc, 1, out)
        self.assertEqual(out.count('**FRAGILE**'), 2, out)

    def test_non_utf8_doc_is_a_usage_error_not_a_finding(self):
        self.cfg['ranking'] = None
        self.cfg['line_refs_into_todo_from'] = ['CLAUDE.md']
        self.write('TODO.md', '# TODO\n')
        with open(os.path.join(self.tree, 'CLAUDE.md'), 'wb') as f:
            f.write(b'\xff\xfe bad bytes\n')
        rc, out = self.run_guard()
        self.assertEqual(rc, 2, out)

    def test_root_discovery_survives_a_git_hook_environment(self):
        """A git hook runs with GIT_DIR (and friends) exported. Then `git -C
        <scriptdir> rev-parse --show-toplevel` answers the SCRIPT dir, and the
        checker looked for scripts/.todo-hygiene.json — found by auth's pre-push
        hook on the first real push, 2026-09-24."""
        subprocess.run(['git', 'init', '-q', self.tree], check=True)
        self.sub()
        self.root('   Ranks `ui/TODO.md:"still open"`.')
        self.write('.todo-hygiene.json', json.dumps(self.cfg))
        dst = os.path.join(self.tree, 'scripts', 'todo_hygiene.py')
        os.makedirs(os.path.dirname(dst), exist_ok=True)
        shutil.copy(SCRIPT, dst)
        env = dict(os.environ, GIT_DIR=os.path.join(self.tree, '.git'))
        p = subprocess.run([sys.executable, dst], cwd=self.tree, env=env,
                           capture_output=True, text=True)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn('1 reference(s) resolved', p.stdout)

    def test_suite_under_a_hook_env_leaves_the_host_repo_config_untouched(self):
        """auth's pre-push (linked worktree, 2026-09-24) exported GIT_DIR=<auth>/
        .git/worktrees/<wt>; a fixture's `git init` inherited it and wrote
        core.bare=true into auth's SHARED config. Runs a git-init test in a child
        under that env and asserts the host config is byte-identical."""
        def host_and_wt(name):
            host = os.path.join(self.tree, name)
            subprocess.run(['git', 'init', '-q', host], check=True)
            subprocess.run(['git', '-C', host, '-c', 'user.name=t', '-c', 'user.email=t@t',
                            'commit', '-q', '--allow-empty', '-m', 'i'], check=True)
            wt = host + '-wt'
            subprocess.run(['git', '-C', host, 'worktree', 'add', '-q', wt], check=True,
                           capture_output=True)
            gd = subprocess.run(['git', '-C', wt, 'rev-parse', '--absolute-git-dir'],
                                check=True, capture_output=True, text=True).stdout.strip()
            return os.path.join(host, '.git', 'config'), wt, dict(os.environ, GIT_DIR=gd)

        def git_bare(cfg):
            return subprocess.run(['git', 'config', '-f', cfg, 'core.bare'],
                                  capture_output=True, text=True).stdout.strip()

        # Positive control: this env really does flip an unguarded `git init`.
        cfg, wt, env = host_and_wt('control')
        subprocess.run(['git', 'init', '-q', os.path.join(self.tree, 'fx')], env=env, check=True)
        self.assertEqual(git_bare(cfg), 'true', 'control no longer reproduces the hazard')

        cfg, wt, env = host_and_wt('host')
        with open(cfg, 'rb') as f:
            before = f.read()
        here = os.path.dirname(os.path.abspath(__file__))
        env['PYTHONDONTWRITEBYTECODE'] = '1'   # the child imports this module; no __pycache__ in the host repo
        p = subprocess.run([sys.executable, '-B', '-m', 'unittest',
                            'test_todo_hygiene.Case.test_root_comes_from_git_when_script_is_deeper'],
                           cwd=here, env=env, capture_output=True, text=True)
        with open(cfg, 'rb') as f:
            self.assertEqual(f.read(), before, 'host repo config changed under a hook env')
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn('Ran 1 test', p.stderr)

    def test_root_flag_overrides(self):
        self.sub()
        self.root('   Ranks `ui/TODO.md:"still open"`.')
        self.write('.todo-hygiene.json', json.dumps(self.cfg))
        p = subprocess.run([sys.executable, SCRIPT, '--root', self.tree],
                           capture_output=True, text=True)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)


if __name__ == '__main__':
    unittest.main(verbosity=2)
