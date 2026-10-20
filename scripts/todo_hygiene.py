#!/usr/bin/env python3
# VENDORED from claude-config skills/todo-hygiene @ f3e83f5f6f29 — edit canonical, not this copy
"""TODO hygiene: fail when a TODO ranking points at a closed item, when anything
points at a TODO item by a LINE NUMBER that will rot on the next edit, and when a
TODO file carries a closed checkbox where its conventions forbid one.

Fleet version (2026-09-24) of RealmID's `scripts/todo-ranking-hygiene.py`. The
canonical copy lives in claude-config `skills/todo-hygiene/scripts/`; every repo
carries a VENDORED copy (see `vendor.sh`) — edit the canonical one, never a copy.
Per-repo conventions come from `<root>/.todo-hygiene.json` (schema in SKILL.md);
an absent config is a usage error, never a silent default.

THE FAULT THIS EXISTS FOR, measured rather than felt. On 2026-08-25 auth's root
ranking held six entries. FOUR of them had already been done, every one recorded
as closed in the sub-repo TODO the entry itself names, and none walked back to
the root file. Closing an item in one file and re-ranking from another are two
different sessions' worth of attention, and nothing connects them. The "walk
back to every file that ranks it" rule IS a hand-maintained subject list.

WHAT IT CHECKS — FOUR HALVES (the name is a lie; renaming them would break every
reference to "half 2").

  HALF 1, the CLOSURE check (only when `ranking` is configured). Entries in the
  current ranking section carry references to the item they rank. For each one
  it reads the referenced line and fails when that line is CLOSED (per
  `closed_marker` / `closed_patterns`) while the ranking entry is still live.

  **CLOSURE is asserted ONLY from a content-verified ANCHOR, never from a bare
  LINE NUMBER.** A struck line found AT a number is not evidence the REFERENCED
  item closed — drift may have walked the number onto somebody else's record.
  A LINE reference that resolves to a closed line is `FRAGILE`, never `CLOSED`.

  **VACUOUS (2026-09-24).** A ranking whose entries resolve 0 references AND have
  0 unresolvable ones verified nothing and has no excuse (it is not "CI lacks the
  sub-repo"): the entries carry no references at all. That used to exit 0 — auth
  ran green for weeks with "6 live entries, 0 resolved, 0 unresolvable". Now it
  fails. 0 resolved with >0 unresolvable stays a pass-with-warning, because a CI
  checkout without nested sub-repos genuinely cannot resolve them.

  HALF 2, the ANCHOR check. A reference written as a LINE NUMBER is correct only
  until somebody inserts a paragraph above it, and it rots SILENTLY. Inside the
  live ranking every line-number-shaped reference fails. Across the rest of the
  TODO file only one thing is provable — a number past the end of its target
  (`DRIFTED`); in-range numbers are COUNTED, never asserted correct.

  HALF 3, the CHECKBOX check. Where `checked_boxes.forbid` is set, a `- [x]`
  outside a closed section is a finding (`CHECKBOX`), with a per-repo remedy
  (convert to a struck record / move to the archive). A convention enforced only
  by whoever remembers it decays: auth swept to zero on 2026-08-25 and was back
  to five by 2026-08-28. `flag_struck_open` adds `STRUCK_OPEN`: `- [ ] ~~…~~`,
  work that is done but was never closed.

  HALF 4, REVERSE references (2026-09-24). Every line-number reference INTO a
  TODO file from the docs listed in `line_refs_into_todo_from` is `FRAGILE`: a
  TODO file churns faster than any other doc, so a number into it cannot
  survive. (Traide: other docs cited `TODO.md` L2034, long since stale.)

REFERENCE FORMS — the table in `FORMS` below is the whole specification.

  `path/to/FILE:"a distinctive needle"`   ANCHOR   — the good form. Resolved by
      SEARCHING the file for the needle, backtick- and whitespace-insensitively.
  `path/to/FILE:64-96`                    RANGE    — `FRAGILE`.
  `path/to/FILE:123`                      LINE     — resolved, and STILL `FRAGILE`.
  `path/to/FILE` L123, `path:L123`        LNUM     — the same, spelled differently.
  `:123`                                  BARE     — no file at all. `UNANCHORED`.
  `line 123` / `lines 566`                PROSE    — longhand, `UNANCHORED`.

  Anything else backtick-wrapped that contains `:<digits>` and no form claims is
  `UNCLASSIFIED` — a finding, never a skip. The answer is a NEW ROW IN THE TABLE,
  not a widened regex.

NOT SCANNED, by design: fenced code blocks (an example of the bad form is not an
instance of it), struck lines (retracted text), a quoted `> - [x]`, anything
between `<!-- todo-hygiene: off -->` and `<!-- todo-hygiene: on -->`, and — for
the line-number sweeps — lines belonging to a closed item or a closed section
(a closed record's `file:line` is history, not a live claim).

KNOWN LIMITATIONS, stated up front rather than discovered:

  1. An entry that closes without ever being referenced is invisible here.
  2. Only the referenced LINE is read, never the block around it — a scan that
     read a following `>` block as the preceding item's body reported every
     closed record as a contradiction in the item above it.
  3. Closure is evaluated only when the referenced path is a TODO file.
  4. NESTED/SIBLING REPOS ARE ABSENT FROM CI's CHECKOUT. With
     `absent_target: note`, a reference whose top-level DIRECTORY is absent is
     UNRESOLVABLE — a note, not a failure (a reported closure that did not happen
     gets a live item closed, which is worse than the decay). Directory present
     but file gone is MISSING, a failure. With `absent_target: fail` (single
     repos, where absence is rot) both are MISSING.
  5. HALF 3 sees only the TODO files this checkout has; the summary prints how
     many it scanned, and lists TODO-looking files it did NOT scan.

Exit 0 = clean, 1 = a finding (see `KINDS`), 2 = usage, config or parse error
(including: a configured ranking that cannot be found — a broken derivation, not
a clean run).
"""

import argparse
import glob
import json
import os
import re
import subprocess
import sys

CONFIG_NAME = '.todo-hygiene.json'

# --- the reference-form table -------------------------------------------------
#
# (name, regex, handler-key). ANCHOR is tried first; the patterns are mutually
# exclusive by construction. `\.[A-Za-z0-9]+` keeps `tenant-picker:55` out of
# LINE and in UNCLASSIFIED, where it belongs until somebody decides what it means.
_PATH = r'[A-Za-z0-9_./-]+\.[A-Za-z0-9]+'

FORMS = (
    ('ANCHOR', re.compile(r'`(' + _PATH + r'):"([^"\n]+)"`'), 'anchor'),
    ('LINE', re.compile(r'`(' + _PATH + r'):(\d+)`'), 'line'),
    ('RANGE', re.compile(r'`(' + _PATH + r'):(\d+-\d+)`'), 'range'),
    ('LNUM', re.compile(r'`(' + _PATH + r')`\s*L(\d+)\b'), 'line'),
    ('LNUM', re.compile(r'`(' + _PATH + r'):L(\d+)`'), 'line'),
    ('BARE', re.compile(r'`():(\d+)`'), 'unanchored'),
    ('PROSE', re.compile(r'\b()lines?\s+(\d+)\b'), 'unanchored'),
)
HANDLER = dict((n, k) for n, _p, k in FORMS)
SWEEP_FORMS = [p for n, p, _k in FORMS if n in ('LINE', 'RANGE', 'LNUM')]

# Every backticked token carrying `:<digits>`. What this matches and no form
# above claims is UNCLASSIFIED. The lookbehind keeps clock times out:
# `2026-08-26T03:49Z` has `:49` preceded by a digit, a line reference never does.
CATCHALL = re.compile(r'`[^`\n]*?(?<![0-9]):\d+[^`\n]*`')

# HALF 4: looser — the citing docs are prose and do not always backtick.
# Only paths that look like a TODO file are kept, so `DECISIONS.md:12` and a date
# after a filename (`TODO.md 2026-08-30`) never match.
REVERSE_FORMS = (
    re.compile(r'`?(' + _PATH + r')`?:(\d+(?:-\d+)?)\b'),
    re.compile(r'`?(' + _PATH + r')`?(?::|\s+)L(\d+)\b'),
    re.compile(r'`?(' + _PATH + r')`?#L(\d+)\b'),              # GitHub link
    re.compile(r'`?(' + _PATH + r')`?,?\s+lines?\s+(\d+)\b'),   # prose
)
# `todo` as its own filename WORD (TODO.md, TODO-ARCHIVE.md, ui_todo.md) — not a
# substring, which matched autodoc.md / mastodon.md / photodocs.md (critic).
TODO_LIKE = re.compile(r'(?:^|[-_.])todo(?:[-_.][^/]*)?\.md$', re.I)

# kind -> one-line meaning printed in the report legend
KINDS = {
    'CLOSED': 'the referenced item is closed — close or re-rank the entry here too',
    'VACUOUS': 'the ranking resolved no reference and none was out of reach — '
               'it verified nothing; anchor each entry to the item it ranks',
    'DRIFTED': 'the line number is past the end of the file',
    'MISSING': 'the referenced file is gone from a tree that should contain it',
    'FRAGILE': 'a line-number reference — rewrite as `path:"needle"`',
    'UNANCHORED': 'a line number with no file — nothing can resolve it',
    'UNCLASSIFIED': 'a `…:N` token no form in FORMS claims — add a row',
    'NO_ANCHOR': 'the anchor needle is not in the file any more',
    'AMBIGUOUS': 'the anchor needle matches more than one line',
    'CHECKBOX': 'a ticked box outside a closed section',
    'STRUCK_OPEN': 'an open box whose head is struck through — done but never '
                   'closed; close it the way this repo closes items',
}

CHECKBOX_ANY = re.compile(r'^\s*- \[[xX]\]')
CHECKBOX_TOP = re.compile(r'^- \[[xX]\]')
STRUCK_OPEN_ANY = re.compile(r'^\s*- \[ \]\s*~~')
STRUCK_OPEN_TOP = re.compile(r'^- \[ \]\s*~~')
HEADING = re.compile(r'^(#{1,6})\s+(.*)')
# A closed record in strike mode: the head itself struck, optionally quoted,
# boxed or numbered — `> ~~**old**~~ CLOSED`, `- [ ] ~~x~~`, `> ~~2. …`.
STRUCK_HEAD = re.compile(r'^\s*(?:>\s*)?(?:- \[.\]\s*|- )?(?:\d+\.\s*)?~~')
OFF, ON = '<!-- todo-hygiene: off -->', '<!-- todo-hygiene: on -->'
SELF_CONTAINED = '<!-- todo-hygiene: self-contained -->'

ENTRY_FORMS = {
    # `N. ` — closed ones are rewritten as `> ~~N.` records and are not matched.
    'numbered': re.compile(r'^(\d+)\.\s'),
    # `- ` or `- [ ] ` — a ticked `- [x]` is closed and not matched.
    'bullet': re.compile(r'^- (?!\[[xX]\])'),
}

# Directories a derived walk never descends into, in any repo. Dot-dirs are
# pruned too (keeps `.scratch/` / `.worktrees/` copies out).
SKIP_DIRS = {'node_modules', 'vendor', 'dist', 'build', 'target', 'coverage',
             '__pycache__'}


# --- config -----------------------------------------------------------------

class ConfigError(Exception):
    pass


DEFAULTS = {
    'closed_patterns': [],
    'closed_sections': [],
    'checked_boxes': {'forbid': False, 'scope': 'any', 'remedy': ''},
    'flag_struck_open': False,
    'line_refs_into_todo_from': [],
    'absent_target': 'fail',
    'absent_dirs': [],
    'fragile_line_refs': 'fail',
    'todo_names': ['TODO.md'],
    'skip_dirs': [],
    'nested_repos': 'skip',
    'dated_history_before': None,
}
REQUIRED = ('todo', 'ranking', 'closed_marker')
ENUMS = {'closed_marker': ('strike', 'checkbox'),
         'absent_target': ('fail', 'note'),
         'fragile_line_refs': ('fail', 'warn'),
         'nested_repos': ('skip', 'scan')}
DATE = re.compile(r'\d{4}-\d{2}-\d{2}')


def load_config(path):
    """Required keys must be present, unknown keys are an error (a typo that
    silently falls back to a default is a guard going blind while green)."""
    try:
        with open(path, encoding='utf-8') as f:
            raw = json.load(f)
    except OSError:
        raise ConfigError('%s not found — every repo states its conventions '
                          'explicitly; see the todo-hygiene SKILL.md' % path)
    except ValueError as e:
        raise ConfigError('%s is not valid JSON: %s' % (path, e))
    unknown = set(raw) - set(REQUIRED) - set(DEFAULTS)
    if unknown:
        raise ConfigError('unknown key(s) in %s: %s' % (path, ', '.join(sorted(unknown))))
    missing = [k for k in REQUIRED if k not in raw]
    if missing:
        raise ConfigError('%s is missing required key(s): %s' % (path, ', '.join(missing)))
    cfg = json.loads(json.dumps(DEFAULTS))
    cfg.update(raw)
    cb = dict(DEFAULTS['checked_boxes'])
    extra = set(raw.get('checked_boxes') or {}) - set(cb)
    if extra:
        raise ConfigError('unknown checked_boxes key(s): %s' % ', '.join(sorted(extra)))
    cb.update(raw.get('checked_boxes') or {})
    cfg['checked_boxes'] = cb
    for key, allowed in ENUMS.items():
        if cfg[key] not in allowed:
            raise ConfigError('%s must be one of %s, got %r' % (key, allowed, cfg[key]))
    if cb['scope'] not in ('any', 'top-level'):
        raise ConfigError('checked_boxes.scope must be "any" or "top-level"')
    r = cfg['ranking']
    if r is not None:
        if not isinstance(r, dict) or not r.get('heading'):
            raise ConfigError('ranking must be null or {"heading": ..., "entry": ...}')
        extra = set(r) - {'heading', 'retired', 'entry'}
        if extra:
            raise ConfigError('unknown ranking key(s): %s' % ', '.join(sorted(extra)))
        r.setdefault('retired', 'strike')
        r.setdefault('entry', 'numbered')
        if r['retired'] != 'strike':
            raise ConfigError('ranking.retired: only "strike" is supported')
        if r['entry'] not in ENTRY_FORMS:
            raise ConfigError('ranking.entry must be one of %s' % sorted(ENTRY_FORMS))
    try:
        cfg['_closed_res'] = [re.compile(p) for p in cfg['closed_patterns']]
    except re.error as e:
        raise ConfigError('closed_patterns: %s' % e)
    dh = cfg['dated_history_before']
    if dh is not None and not (isinstance(dh, str) and re.fullmatch(r'\d{4}-\d{2}-\d{2}', dh)):
        raise ConfigError('dated_history_before must be a YYYY-MM-DD string or null, got %r' % (dh,))
    return cfg


def find_root(arg):
    """--root, else the git toplevel of the SCRIPT's directory (so a vendored
    copy at any depth finds its repo), else the script dir's parent."""
    if arg:
        return os.path.abspath(arg)
    here = os.path.dirname(os.path.abspath(__file__))
    # STRIP GIT_* FIRST. A git hook exports GIT_DIR (and GIT_INDEX_FILE, …), and
    # with GIT_DIR set `rev-parse --show-toplevel` answers the -C dir (or fails)
    # instead of the repo root — the checker then looked for
    # scripts/.todo-hygiene.json. Found by auth's pre-push hook, 2026-09-24.
    env = {k: v for k, v in os.environ.items() if not k.startswith('GIT_')}
    try:
        p = subprocess.run(['git', '-C', here, 'rev-parse', '--show-toplevel'],
                           capture_output=True, text=True, env=env)
        if p.returncode == 0 and p.stdout.strip():
            return p.stdout.strip()
    except OSError:
        pass
    return os.path.dirname(here)


# --- line handling ------------------------------------------------------------

def _split_lines(text):
    """Split into lines WITHOUT the phantom trailing entry. `split('\\n')` yields a
    final '' for every file ending in a newline, so `len()` reported N+1 and
    accepted line N+1 as in range — the PERMISSIVE direction. Deliberately NOT
    `splitlines()`: that also breaks on \\r, \\f, \\x0b and U+2028, numbering a
    file differently from every editor and `wc -l`."""
    out = text.split('\n')
    if out and out[-1] == '':
        out.pop()
    return out


class UsageError(Exception):
    pass


def mask(lines, name='?'):
    """A copy with every line the scans must not read blanked to '': fenced code
    (and its fence lines) and `todo-hygiene: off` regions. Blanking rather than
    dropping keeps line numbers true.

    A marker INSIDE a fence is an example, not a marker — a rules header
    documents the opt-out that way, and reading it as live silently blanked
    everything below it (critic, 2026-09-24). An OFF with no ON is an error:
    a region that never closes is a guard switched off by accident."""
    out, fenced, off_at = [], False, None
    for n, ln in enumerate(lines, 1):
        s = ln.strip()
        if off_at is not None:
            if s == ON:
                off_at = None
            out.append('')
            continue
        if ln.lstrip().startswith('```'):
            fenced = not fenced
            out.append('')
            continue
        if fenced:
            out.append('')
            continue
        if s == OFF:
            off_at = n
            out.append('')
            continue
        out.append(ln)
    if off_at is not None:
        raise UsageError('%s:%d opens a `todo-hygiene: off` region that is never '
                         'closed with `%s`' % (name, off_at, ON))
    return out


def read_lines(root, rel):
    try:
        return _split_lines(open(os.path.join(root, rel), encoding='utf-8').read())
    except UnicodeDecodeError as e:
        raise UsageError('%s is not UTF-8: %s' % (rel, e))


def _indent(ln):
    return len(ln) - len(ln.lstrip())


def closed_section_lines(lines, cfg):
    """1-based line numbers inside a heading named in `closed_sections`, up to
    the next heading of the same or a higher level."""
    names = cfg['closed_sections']
    out, level = set(), None
    for i, ln in enumerate(lines):
        m = HEADING.match(ln)
        if m:
            lv = len(m.group(1))
            if level is not None and lv <= level:
                level = None
            if level is None and any(n in m.group(2) for n in names):
                level = lv
                continue
        if level is not None:
            out.add(i + 1)
    return out


def is_closed_line(ln, cfg):
    """Closure as this repo spells it. `closed_marker: strike` — the line carries
    `~~`; `checkbox` — it is a ticked box. `closed_patterns` add to either (e.g.
    AutoMahn's `ANSWERED <date>`). A ✅ inside an open item is NOT closure."""
    if cfg['closed_marker'] == 'strike' and '~~' in ln:
        return True
    if cfg['closed_marker'] == 'checkbox' and CHECKBOX_ANY.match(ln):
        return True
    return any(r.search(ln) for r in cfg['_closed_res'])


def closed_item_lines(lines, cfg):
    """1-based line numbers of closed ITEMS plus their continuation — a `- [x]`
    (in any mode), a closed_patterns line, or (strike mode) a record whose head
    is struck, and what hangs under it: indented lines, or for a `>` record the
    `>` lines that follow."""
    out, item_indent, quoted = set(), None, False
    for i, ln in enumerate(lines):
        if item_indent is not None:
            if quoted and ln.lstrip().startswith('>'):
                out.add(i + 1)
                continue
            if not quoted and (not ln.strip() or _indent(ln) > item_indent):
                out.add(i + 1)
                continue
            item_indent = None
        if (CHECKBOX_ANY.match(ln) or any(r.search(ln) for r in cfg['_closed_res'])
                or (cfg['closed_marker'] == 'strike' and STRUCK_HEAD.match(ln))):
            out.add(i + 1)
            item_indent = _indent(ln)
            quoted = ln.lstrip().startswith('>')
    return out


# --- HALF 1 / HALF 2 in the ranking ------------------------------------------

def current_ranking(lines, heading):
    """The newest un-retired ranking section, as (start, end) line indices.
    Retired rankings are struck through in their own heading."""
    start = None
    for i, ln in enumerate(lines):
        if ln.startswith('## ') and heading in ln and '~~' not in ln:
            start = i
            break
    if start is None:
        return None
    for j in range(start + 1, len(lines)):
        if lines[j].startswith('## '):
            return start, j
    return start, len(lines)


def parse_entries(lines, start, end, entry_re):
    """Ranked entries in [start, end), each with its indented continuation body.
    A body ends at the first non-blank line in column 0 — which is what a closed
    record or the next entry is (limitation 2)."""
    entries, cur, n = [], None, 0
    for i in range(start, end):
        ln = lines[i]
        m = entry_re.match(ln)
        if m:
            n += 1
            num = m.group(1) if m.groups() else str(n)
            cur = {'num': num, 'line': i + 1, 'body': [ln]}
            entries.append(cur)
            continue
        if cur is None:
            continue
        if ln.strip() and not ln[:1].isspace():
            cur = None
            continue
        cur['body'].append(ln)
    return entries


def scan_refs(body):
    """Classify every line-number-shaped reference in an entry body. Yields
    (form, path, target, text). Struck lines are retracted text and dropped."""
    out = []
    for ln in body:
        if '~~' in ln:
            continue
        claimed = []
        for name, pat, _key in FORMS:
            for m in pat.finditer(ln):
                out.append((name, m.group(1), m.group(2), m.group(0)))
                claimed.append(m.span())
        # REFUSE TO DROP WHAT NO FORM CLAIMED.
        for m in CATCHALL.finditer(ln):
            s, e = m.span()
            if any(s < ce and cs < e for cs, ce in claimed):
                continue
            out.append(('UNCLASSIFIED', '', '', m.group(0)))
    return out


def read_target(root, path, cache, cfg):
    """('OK', lines) | ('UNRESOLVABLE', None) | ('MISSING', None). See
    limitation 4: under `absent_target: note`, a target whose top-level
    directory is ABSENT and LISTED in `absent_dirs` is unresolvable. An absent
    directory that is not listed is MISSING — a typo'd `uii/` must not pass as
    a nested repo (critic, 2026-09-24). A new sub-repo nobody listed fails
    loudly, which is the safe direction."""
    path = os.path.normpath(path).replace(os.sep, '/')
    if path not in cache:
        full = os.path.join(root, path)
        cache[path] = read_lines(root, path) if os.path.isfile(full) else None
    sub = cache[path]
    if sub is not None:
        return 'OK', sub
    repo_dir = path.split('/')[0] if '/' in path else ''
    if (cfg['absent_target'] == 'note' and repo_dir in cfg['absent_dirs']
            and not os.path.isdir(os.path.join(root, repo_dir))):
        return 'UNRESOLVABLE', None
    return 'MISSING', None


def norm(text):
    """Backtick- and whitespace-insensitive form, for matching an anchor needle."""
    return re.sub(r'\s+', ' ', text.replace('`', '')).strip()


def is_todo_path(path, cfg):
    base = path.rsplit('/', 1)[-1]
    return base in cfg['todo_names'] or bool(TODO_LIKE.search(base))


def check_entry(root, entry, cache, cfg):
    """(findings, notes, resolved_count) for one ranked entry. Every form maps to
    exactly one handler and every outcome to exactly one kind in `KINDS`."""
    findings, notes, resolved = [], [], 0
    seen = set()
    todo = cfg['todo']
    for form, path, target, text in scan_refs(entry['body']):
        if (form, path, target, text) in seen:
            continue
        seen.add((form, path, target, text))
        handler = HANDLER.get(form)

        if form == 'UNCLASSIFIED':
            findings.append((entry, text, 'UNCLASSIFIED',
                             'no row in FORMS matches this shape'))
            continue
        if handler == 'range':
            findings.append((entry, text, 'FRAGILE',
                             'a line RANGE resolves to no single item and rots '
                             'at both ends — rewrite as `%s:"a distinctive '
                             'needle"`' % path))
            continue
        if handler == 'unanchored':
            findings.append((entry, text, 'UNANCHORED',
                             'a line number with no file — write '
                             '`path/to/FILE:"a distinctive needle"`'))
            continue

        state, sub = read_target(root, path, cache, cfg)
        if state == 'UNRESOLVABLE':
            notes.append((entry, text, path))
            # HALF 2 IS SHAPE-ONLY AND STILL APPLIES where the file is absent.
            if handler == 'line':
                findings.append((entry, text, 'FRAGILE',
                                 'a line number rots on the next edit above it '
                                 '— rewrite as `%s:"a distinctive needle"`' % path))
            continue
        if state == 'MISSING':
            findings.append((entry, text, 'MISSING', '%s does not exist' % path))
            continue

        if handler == 'anchor':
            needle = norm(target)
            # EXCLUDE THE CITING ENTRY'S OWN LINES: an anchor into the ranking
            # file itself would otherwise match its own quoted needle.
            entry_start = entry['line']
            entry_end = entry_start + len(entry['body']) - 1
            hits = [i + 1 for i, ln in enumerate(sub) if needle in norm(ln)
                    and not (os.path.normpath(path) == os.path.normpath(todo) and entry_start <= i + 1 <= entry_end)]
            if not hits:
                findings.append((entry, text, 'NO_ANCHOR',
                                 'no line of %s contains that text' % path))
                continue
            if len(hits) > 1:
                findings.append((entry, text, 'AMBIGUOUS',
                                 'matches %d lines of %s (%s) — lengthen the '
                                 'needle' % (len(hits), path,
                                             ', '.join(map(str, hits[:5])))))
                continue
            resolved += 1
            line = sub[hits[0] - 1]
            if is_todo_path(path, cfg) and is_closed_line(line, cfg):
                findings.append((entry, text, 'CLOSED', line.strip()))
            continue

        # handler == 'line'. NEVER reports CLOSED (see the docstring).
        n = int(target)
        if n < 1 or n > len(sub):
            findings.append((entry, text, 'DRIFTED',
                             '%s has %d lines' % (path, len(sub))))
            continue
        resolved += 1
        if is_todo_path(path, cfg) and is_closed_line(sub[n - 1], cfg):
            findings.append((entry, text, 'FRAGILE',
                             'resolved today, and the line at this position '
                             'IS closed — but a line number is drift-prone, so '
                             'that is not proof this is the same item: %s. '
                             'Rewrite as `%s:"a distinctive needle"` and '
                             're-check by hand.' % (sub[n - 1].strip(), path)))
            continue
        findings.append((entry, text, 'FRAGILE',
                         'resolved today, but a line number rots on the next '
                         'edit above it — rewrite as `%s:"a distinctive '
                         'needle"`' % path))
    return findings, notes, resolved


def scan_whole_file_refs(root, lines, skip, cache, cfg):
    """HALF 2, WIDENED: every LINE/RANGE/LNUM reference in the whole TODO file
    outside `skip` (lines check_entry already read, plus closed items/sections).
    Can prove ONE thing — a number past the end of its target (`DRIFTED`); an
    in-range hit is COUNTED, never asserted correct. Returns (findings, inrange)."""
    findings, inrange = [], 0
    for i, ln in enumerate(lines):
        if (i + 1) in skip or '~~' in ln:
            continue
        for pat in SWEEP_FORMS:
            for m in pat.finditer(ln):
                path = m.group(1)
                state, sub = read_target(root, path, cache, cfg)
                if state != 'OK':
                    continue
                bad = [t for t in m.group(2).split('-')
                       if int(t) < 1 or int(t) > len(sub)]
                if bad:
                    findings.append((i + 1, m.group(0), path, len(sub)))
                else:
                    inrange += 1
    return findings, inrange


# --- HALF 3 -----------------------------------------------------------------

def walk(root, cfg):
    """(todo_files, not_scanned, nested): files named in `todo_names`, DERIVED by
    walking the tree; TODO-looking files that are NOT scanned; and nested repos
    pruned. All three are listed in the report, never silent.

    NESTED REPOS (Traide, 2026-09-24): a subdirectory with its own `.git` is
    another repo's backlog. Under `nested_repos: skip` (default) the walk does
    not enter it — the parent's gate must not fail over a backlog it does not
    own, and a CI checkout (which lacks the clone) must not disagree with the
    laptop. `scan` keeps auth's deliberate stronger-locally behaviour. Anchors
    INTO a nested repo still resolve either way: this is only the walk."""
    skip = SKIP_DIRS | set(cfg['skip_dirs'])
    found, other, nested = [], [], []
    for dirpath, dirnames, filenames in os.walk(root):
        keep = []
        for d in sorted(dirnames):
            if d.startswith('.') or d in skip:
                continue
            if (cfg['nested_repos'] == 'skip'
                    and os.path.exists(os.path.join(dirpath, d, '.git'))):
                nested.append(os.path.relpath(os.path.join(dirpath, d), root)
                              .replace(os.sep, '/') + '/')
                continue
            keep.append(d)
        dirnames[:] = keep
        for fn in filenames:
            rel = os.path.relpath(os.path.join(dirpath, fn), root).replace(os.sep, '/')
            if fn in cfg['todo_names']:
                found.append(rel)
            elif TODO_LIKE.search(fn):
                other.append(rel)
    return sorted(found), sorted(other), sorted(nested)


def scan_checkboxes(root, paths, cfg):
    """HALF 3: [(path, lineno, text, kind)] for CHECKBOX and STRUCK_OPEN."""
    cb = cfg['checked_boxes']
    top = cb['scope'] == 'top-level'
    box = CHECKBOX_TOP if top else CHECKBOX_ANY
    struck = STRUCK_OPEN_TOP if top else STRUCK_OPEN_ANY
    out = []
    for rel in paths:
        try:
            lines = mask(read_lines(root, rel), rel)
        except OSError:
            continue
        allowed = closed_section_lines(lines, cfg)
        for n, ln in enumerate(lines, 1):
            if n in allowed:
                continue
            if cb['forbid'] and box.match(ln):
                out.append((rel, n, ln.strip(), 'CHECKBOX'))
            elif cfg['flag_struck_open'] and struck.match(ln):
                out.append((rel, n, ln.strip(), 'STRUCK_OPEN'))
    return out


# --- HALF 4 -----------------------------------------------------------------

def scan_reverse_refs(root, cfg, cache):
    """HALF 4: line-number references into a TODO file from the configured docs.
    Returns (findings [(rel, n, text, kind, detail)], notes [(rel, n, text, path)],
    files_scanned, history_skipped).

    DATED HISTORY (Traide, 2026-09-24): with `dated_history_before`, lines under
    a heading whose first date is earlier than the cutoff are skipped — a dated
    log entry records where an item sat THAT day, and logs are not rewritten.
    Entries dated on/after the cutoff are checked; skips are COUNTED in the
    report, never silent."""
    cutoff = cfg['dated_history_before']
    files = set()
    for pattern in cfg['line_refs_into_todo_from']:
        for full in glob.glob(os.path.join(root, pattern), recursive=True):
            if os.path.isfile(full):
                files.add(os.path.relpath(full, root).replace(os.sep, '/'))
    files = sorted(f for f in files if f.rsplit('/', 1)[-1] not in cfg['todo_names'])
    findings, notes, history = [], [], 0
    for rel in files:
        lines = mask(read_lines(root, rel), rel)
        skip = closed_section_lines(lines, cfg) | closed_item_lines(lines, cfg)
        in_history = False
        for n, ln in enumerate(lines, 1):
            h = HEADING.match(ln)
            if h and cutoff:
                d = DATE.search(h.group(2))
                in_history = bool(d) and d.group(0) < cutoff
            if n in skip or '~~' in ln:
                continue
            if in_history:
                history += sum(1 for pat in REVERSE_FORMS for m in pat.finditer(ln)
                               if is_todo_path(m.group(1), cfg))
                continue
            seen = set()
            for pat in REVERSE_FORMS:
                for m in pat.finditer(ln):
                    path, target = m.group(1), m.group(2)
                    if not is_todo_path(path, cfg) or (path, target) in seen:
                        continue
                    seen.add((path, target))
                    state, sub = read_target(root, path, cache, cfg)
                    if state == 'UNRESOLVABLE':
                        notes.append((rel, n, m.group(0), path))
                    elif state == 'MISSING':
                        findings.append((rel, n, m.group(0), 'MISSING',
                                         '%s does not exist' % path))
                    elif any(int(t) > len(sub) for t in target.split('-')):
                        findings.append((rel, n, m.group(0), 'DRIFTED',
                                         '%s has %d lines' % (path, len(sub))))
                    else:
                        findings.append((rel, n, m.group(0), 'FRAGILE',
                                         'a TODO file churns faster than any '
                                         'other doc — cite the item as '
                                         '`%s:"a distinctive needle"`' % path))
    return findings, notes, len(files), history


# --- main -------------------------------------------------------------------

def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split('\n')[0])
    ap.add_argument('--root', help='repo root (default: git toplevel of this script)')
    ap.add_argument('--config', help='config path (default: <root>/%s)' % CONFIG_NAME)
    args = ap.parse_args(argv)

    root = find_root(args.root)
    try:
        cfg = load_config(args.config or os.path.join(root, CONFIG_NAME))
    except ConfigError as e:
        print('::error::%s' % e, file=sys.stderr)
        return 2

    todo_rel = cfg['todo']
    todo = os.path.join(root, todo_rel)
    if not os.path.isfile(todo):
        print('::error::%s does not exist' % todo, file=sys.stderr)
        return 2
    lines = mask(read_lines(root, todo_rel), todo_rel)
    closed = closed_section_lines(lines, cfg) | closed_item_lines(lines, cfg)

    cache, failures, notes, resolved = {}, [], [], 0
    entries, start, self_contained = [], None, 0
    r = cfg['ranking']
    if r is not None:
        span = current_ranking(lines, r['heading'])
        if span is None:
            # REFUSE TO REPORT NOTHING AS FINE.
            print('::error::no un-retired "## %s" section found in %s — the '
                  'heading convention changed; this is a broken derivation, not '
                  'a clean run' % (r['heading'], todo_rel), file=sys.stderr)
            return 2
        start, end = span
        entries = parse_entries(lines, start, end, ENTRY_FORMS[r['entry']])
        if not entries:
            print('::error::the current ranking contains no entries — either '
                  'every entry is closed (write a new ranking) or the entry '
                  'convention changed', file=sys.stderr)
            return 2
        for e in entries:
            f, n, rs = check_entry(root, e, cache, cfg)
            failures += f
            notes += n
            resolved += rs
            # PER ENTRY, not per ranking: one anchored entry must not cover for
            # a sibling that references nothing (critic, 2026-09-24).
            # An entry that IS the item has nothing to point at; it must SAY so
            # (SELF_CONTAINED), because silence is indistinguishable from a
            # forgotten anchor. The count is printed, never hidden.
            if any(SELF_CONTAINED in ln for ln in e['body']):
                self_contained += 1
                continue
            if rs == 0 and not n:
                failures.append((e, '', 'VACUOUS',
                                 'this entry resolved no reference and none was '
                                 'out of reach — nothing here was verified'))

    checked = set(closed)
    for e in entries:
        checked.update(range(e['line'], e['line'] + len(e['body'])))
    wf_findings, wf_inrange = scan_whole_file_refs(root, lines, checked, cache, cfg)
    for lineno, text, path, length in wf_findings:
        failures.append(({'num': 'outside-ranking', 'line': lineno}, text,
                         'DRIFTED', '%s has %d lines' % (path, length)))

    todo_files, not_scanned, nested = walk(root, cfg)
    boxes = scan_checkboxes(root, todo_files, cfg)
    rv_findings, rv_notes, rv_files, rv_history = scan_reverse_refs(root, cfg, cache)
    rv_warn = cfg['fragile_line_refs'] == 'warn'
    rv_fail = [f for f in rv_findings if not (rv_warn and f[3] == 'FRAGILE')]
    rv_soft = [f for f in rv_findings if rv_warn and f[3] == 'FRAGILE']

    print('## TODO hygiene')
    print()
    if r is None:
        print('Ranking — no ranking configured; half 1 skipped.')
    else:
        print('Ranking at `%s:%d` — %d live entr%s, %d reference(s) resolved, '
              '%d unresolvable, %d self-contained.' % (todo_rel, start + 1, len(entries),
                                    'y' if len(entries) == 1 else 'ies',
                                    resolved, len(notes), self_contained))
    print('Whole-file sweep (half 2) — %d out-of-range line reference(s) '
          '(provable), %d in-range line reference(s) elsewhere in the file (NOT '
          'verified — only that it has not yet drifted past the end of its file).'
          % (len(wf_findings), wf_inrange))
    print('Checkbox scan — %d TODO file(s) in this checkout (%s), %d `- [x]` '
          'record(s)%s.' % (len(todo_files), ', '.join('`%s`' % f for f in todo_files),
                            sum(1 for b in boxes if b[3] == 'CHECKBOX'),
                            '' if cfg['checked_boxes']['forbid'] else ' (allowed here)'))
    print('Reverse-reference scan (half 4) — %d doc(s) scanned, %d line-number '
          'reference(s) into a TODO file, %d skipped as dated history.' % (rv_files, len(rv_findings) + len(rv_notes), rv_history))
    if nested:
        print('Nested repo(s) not scanned (own .git; `nested_repos: skip`): %s.' % ', '.join('`%s`' % d for d in nested))
    if not_scanned:
        print('TODO-looking file(s) not scanned (not in `todo_names`): %s.'
              % ', '.join('`%s`' % f for f in not_scanned))
    print()

    # Print these BEFORE the verdict: a green tick must still say how much of the
    # tree this run could actually see.
    for e, text, path in notes:
        print('- ⚠️ **entry %s** (`%s:%d`) references %s — **not in this '
              'checkout**: `%s/` is absent from this tree (a nested repo or '
              'sibling clone). NOT a closure; run this script where it exists.'
              % (e['num'], todo_rel, e['line'], text, path.split('/')[0]))
        print('::notice file=%s,line=%d::entry %s\'s reference %s is unresolvable '
              'here — %s/ is not in this checkout'
              % (todo_rel, e['line'], e['num'], text, path.split('/')[0]))
    for rel, n, text, path in rv_notes:
        print('- ⚠️ **`%s:%d`** references %s — **not in this checkout**; not '
              'checked.' % (rel, n, text))
    for rel, n, text, kind, detail in rv_soft:
        print('- ⚠️ **`%s:%d`** — **%s** %s: %s' % (rel, n, kind, text, detail))
        print('::warning file=%s,line=%d::%s %s — %s' % (rel, n, kind, text, detail))
    if notes or rv_notes or rv_soft:
        print()

    remedy = cfg['checked_boxes']['remedy']
    for rel, n, text, kind in boxes:
        detail = remedy if kind == 'CHECKBOX' else KINDS['STRUCK_OPEN']
        print('- ❌ **`%s:%d`** — **%s** %s — %s' % (rel, n, kind, text[:120], detail))
        print('::error file=%s,line=%d::%s — %s' % (rel, n, kind, detail))
    for rel, n, text, kind, detail in rv_fail:
        print('- ❌ **`%s:%d`** — **%s** %s: %s' % (rel, n, kind, text, detail))
        print('::error file=%s,line=%d::%s %s — %s' % (rel, n, kind, text, detail))
    if boxes or rv_fail:
        print()

    if not failures and not boxes and not rv_fail:
        if r is None:
            print('- ✅ clean.')
        elif resolved:
            print('- ✅ every ranked entry that COULD be resolved points at an '
                  'item that is still open, and every reference is anchored to '
                  'text rather than to a line number.')
        else:
            print('- ⚠️ **this run resolved no references at all** (every one is '
                  'out of this checkout), so half 1 verified nothing here. Half 2 '
                  'is shape-only and did run. Green here does not mean the '
                  'ranking is current — only a run with the targets present can '
                  'say that.')
        return 0

    for e, text, kind, detail in failures:
        print('- ❌ **entry %s** (`%s:%d`) — **%s** %s: %s'
              % (e['num'], todo_rel, e['line'], kind, text, detail))
        print('::error file=%s,line=%d::ranked entry %s: %s %s — %s'
              % (todo_rel, e['line'], e['num'], kind, text, detail))
    print()
    kinds = {k for _e, _t, k, _d in failures} | {b[3] for b in boxes} \
        | {f[3] for f in rv_fail}
    for kind in sorted(kinds):
        meaning = KINDS[kind]
        if kind == 'CHECKBOX' and remedy:
            meaning += ' — ' + remedy
        print('- `%s` — %s' % (kind, meaning))
    print()
    if failures or rv_fail:
        print('**A ranking is a snapshot with a shelf life, and a line number is '
              'a snapshot too.** Point at an item with '
              '`path/to/FILE:"a distinctive needle"`, which survives every edit '
              'above it and fails LOUDLY when the item itself is rewritten.')
    return 1


if __name__ == '__main__':
    try:
        sys.exit(main())
    except UsageError as e:
        # rc 2, never 1: an unreadable input is not a finding (critic, 2026-09-24).
        print('::error::%s' % e, file=sys.stderr)
        sys.exit(2)
