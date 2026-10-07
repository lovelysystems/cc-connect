# Lovely Downstream Fork

This repository is Lovely Systems' downstream fork of
[cc-connect](https://github.com/chenhg5/cc-connect). It carries upstream plus a
small set of Lovely-maintained changes (most notably the Microsoft Teams
connector). This document describes how the fork is versioned, kept in sync, and
wound down. `CHANGES.md` is the downstream changelog.

## Goal: dissolution

**This fork is intended to be temporary.** The aim is to upstream every downstream
change over time and shrink the delta to zero — at which point `downstream` is
deleted and we deploy upstream releases directly. The versioning and files here are
scaffolding that exists only while the delta is non-empty; none of it is a
long-term parallel product.

**Progress metric.** The objective delta is exactly the commits `git cherry`
reports as `+`:

```
git cherry -v <upstream-base-tag> downstream   # + = downstream-only, - = already upstream
```

Each downstream change ends in one of:

1. **Merged upstream** → dropped from the delta at the next re-base onto a release
   that contains it (see "Re-basing" below).
2. **Merged in modified form** → upstream's version is canonical; take theirs.
3. **Not upstreamable** → the only thing that keeps the fork alive. Today the open
   question is the **Teams connector** — if upstream accepts it, full dissolution
   is achievable; if not, `downstream` shrinks to Teams-only rather than to zero.

**Keep dissolution cheap.** Author every downstream commit PR-ready (upstream
conventions, tests, atomic, no downstream-only entanglement) so upstream can merge
it *verbatim* — a verbatim merge is auto-detected by patch-id and drops with zero
conflict. Never accumulate un-upstreamable glue (deployment hacks, config-specific
tweaks); those become permanent anchors that prevent teardown.

**Teardown (when `git cherry` shows no `+` commits).**

1. Delete `CHANGES.md` and `DOWNSTREAM.md`.
2. Reset `Makefile:VERSION` to upstream's value.
3. Delete the `downstream` branch; deploy upstream tags directly via the `main`
   mirror.

## Branches

- **`main`** — a clean mirror of upstream's `main`. No Lovely commits land here;
  it exists only to track upstream.
- **`downstream`** — the deployed integration branch: an upstream **release base**
  plus Lovely's commits on top. This is what gets built and shipped.

## What we own vs. what upstream owns

Upstream artifacts are never edited downstream, so upstream syncs merge cleanly:

| File | Owner |
|------|-------|
| `README.md`, `README.zh-CN.md` | upstream |
| `CHANGELOG.md` | upstream |
| `DOWNSTREAM.md` (this file) | downstream |
| `CHANGES.md` | downstream |

A downstream change that would otherwise want a `CHANGELOG.md` entry goes in
`CHANGES.md` instead.

## Base policy

`downstream` is based on a specific upstream **release tag**, not on a moving
`main` commit and not on a fork-invented version number. The current base is the
one baked into the fork version (below) and is the top `## <base>` section in
`CHANGES.md`.

Note on upstream's layout: upstream cuts each release (`v1.4.x`, `v1.5.0-beta.x`)
on its own branch off `main`; those release lineages are divergent, not linear.
Moving the base from one upstream release to another is therefore a real merge,
not a fast-forward — treat a base change as a deliberate step.

## Versioning and tags

The fork version is **independent of upstream's release numbers** and bakes in the
upstream base it is built on:

```
<upstream-base>-ls.<N>
```

- `<upstream-base>` — the upstream release tag `downstream` is based on
  (e.g. `v1.5.0-beta.2`).
- `-ls.<N>` — the Lovely revision on that base, starting at `1` and incrementing
  for each downstream release **on the same base**. Re-basing onto a new upstream
  release resets `N` to `1`.

Example: `v1.5.0-beta.2-ls.1`, then `v1.5.0-beta.2-ls.2`; after re-basing onto a
newer upstream release, `<new-base>-ls.1`.

The hyphen form (`-ls.N`) is used rather than SemVer build metadata (`+ls.N`)
because `+` is not valid in Docker image tags, and the fork is deployed as an
image. `Makefile:VERSION` carries the same string so the binary reports it.

## Changelog shape (`CHANGES.md`)

`CHANGES.md` is grouped by **upstream base** (newest base first), because the delta
only changes at a re-base — within a base it only grows:

```
## <upstream-base>            # e.g. ## v1.5.0-beta.2 — the base section
### Unreleased                # work not yet cut into a fork tag
#### Fix / #### Feature / #### Removal
- **scope**: description (upstream: <status>)
### <date> / <base>-ls.<N>    # a cut fork release, newest first
#### Fix / #### Feature / #### Removal
- ...
```

- **Entry style** mirrors upstream's `CHANGELOG.md`: `- **scope**: description`,
  scope lowercase (`teams`, `claudecode`, `core`, `display`).
- **Inline upstream status** — `(upstream: <status>)` where status is `pending` (not
  submitted) · `submitted chenhg5/cc-connect#N` (PR open) · `merged` ·
  `superseded by chenhg5/cc-connect#N` (upstream fixed it in another PR) ·
  `withdrawn chenhg5/cc-connect#N` (PR closed by us; the change is dropped
  downstream). Upstream and backport markers show today's status and sit on a
  change's newest listing in the file: a carried change → its entry in the current
  base section; a change that graduated at a re-base → its last entry in the old base
  section, which keeps its final marker (`merged` /
  `superseded by chenhg5/cc-connect#N`); a withdrawn change → its `#### Removal`
  entry. Older base sections carry no other markers. This is the one fork-specific
  deviation from house style; it replaces a separate tracking table so there is a
  single source of truth. `git cherry` is the objective "merged?" check; the marker
  adds the PR link and the not-yet-submitted state git cannot see.
- **Reading it:** a tag's contents = read within its base section down to that tag;
  the current delta = the top base's `### Unreleased` plus its released sections;
  an item's upstream status = the marker on its newest listing.

## Backporting upstream PRs

Sometimes we carry an **in-flight upstream PR** early — a fix that's merged into a
newer upstream release than our base, or an open PR we don't want to wait for. It's
part of the downstream delta like our own patches, but with different semantics: it
is **not ours to upstream** (it's already an upstream PR by its author), so it needs
no action from us — it drops when that PR lands upstream and we re-base.

- **Cherry-pick the PR's commit(s) pristine** — preserve the original author and the
  exact diff. `git cherry` / patch-id then recognizes it as merged once it lands
  upstream, so it drops cleanly at the next re-base with zero conflict. If a hunk
  lands with different surrounding lines (e.g. in `CHANGELOG.md`), patch-id does not
  match; re-base step 1 drops it by hand. Do **not** fold a `CHANGES.md` edit into
  the cherry-picked commit (that changes the diff and breaks patch-id matching) — put
  the changelog line in a separate commit.
- **Rebase-merge the PR into `downstream`, never squash** — this overrides the
  squash default. A squash folds the cherry-pick into the `CHANGES.md` commit next
  to it, and re-base step 1 can no longer drop the backport on its own. The same
  applies to a PR carrying a revert.
- **Mark it** `(backport: chenhg5/cc-connect#N)` in `CHANGES.md`, so it's clear
  it's carried, not authored here, and that it's tracked by *their* PR, not ours.

```
git fetch upstream pull/<N>/head
git cherry-pick <commit>          # pristine; author preserved
# separate commit: add the (backport: chenhg5/cc-connect#N) CHANGES.md line
```

## Cutting a fork release (same base)

Between re-bases the delta only appends — no copying.

1. In `CHANGES.md`, rename the base's `### Unreleased` to
   `### <date> / <upstream-base>-ls.<N>` and open a fresh empty `### Unreleased`.
2. Set `Makefile:VERSION` to `<upstream-base>-ls.<N>`.
3. Commit, then tag `<upstream-base>-ls.<N>`.
4. Push `downstream` and the tag.

## Re-basing onto a new upstream release

`downstream` is a **clean patch set** — `<base>` + our commits, linear. Moving to a
newer upstream release **rebases that patch set onto the new tag**; it does not
merge. (A merge drags the old base's commits along — that is how stray upstream
commits leak in — and it leaves `git cherry` polluted.) Rebasing rewrites
`downstream` history, so it ends in a **force-push** — that is the accepted cost of
keeping the branch clean.

1. Replay our commits onto the new release tag:
   `git rebase --onto <new-base> <old-base> downstream`. If the old base is tangled
   (e.g. a previous merge is in the way), rebuild instead: branch from `<new-base>`
   and `git cherry-pick` our commits in order — that picks *only* our commits and
   avoids re-applying the new base's own commits. Leave a withdrawn change's
   commit and its revert out of the replay, and a backport commit once its
   upstream PR is in the new base.
2. Resolve `CHANGELOG.md` conflicts to the base (`--ours`) — fork changes live in
   `CHANGES.md`, so `CHANGELOG.md` stays pure upstream. (`git rerere` replays these
   resolutions across attempts.)
3. Run `git cherry -v <new-base> downstream`; it must list **only** our commits. An
   item that graduated upstream (merged or superseded) simply won't be in the replay
   and gets no new entry; its entry in the old base section keeps its final marker. A
   withdrawn item gets a `#### Removal` entry, with its `withdrawn` marker, in the new
   base's `### Unreleased`, so it lands in the new base's first release.
4. In `CHANGES.md`, add a new `## <new-base>` section and copy the surviving entries,
   with their markers, into its `### Unreleased`; `#### Removal` entries stay in their
   base section. Then strip the markers from the old base section, except on graduated
   entries and `#### Removal` entries, which keep their final marker.
5. Build + test, then `git push --force-with-lease origin downstream`.
6. Cut the first release on the new base (`<new-base>-ls.1`) per "Cutting a fork
   release" above.
