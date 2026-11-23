# PocketOne

PocketOne is PocketBase plus extensions. **PocketBase's own code ("core") changes only for hooks that let `ext/`
extend or override behavior; the features themselves live in `ext/`.** Core is every path outside what PocketOne owns:
`ext/`, `site/`, `examples/pocketone/`, `docs/`, `README.md`, `CONTRIBUTING.md`, `AGENTS.md`, `CLAUDE.md`, `CHANGELOG.md`,
`CHANGELOG.pocketbase.md`, `.github/`, `.claude/`, `.claude-plugin/`, `agent/`. Each core change is marked
`// PocketOne:` so upstream merges stay easy.

Words: everything in `ext/` is an **extension**, never a "plugin". **Standard extensions** ship in every `pocketone`
binary (`examples/pocketone`); the rest of `ext/` are **community extensions**. "Plugin" means only upstream's
`plugins/` (jsvm, migratecmd, ghupdate) and Pages' own plugin system in `ext/pages` (`+config.js` `plugins`).

## Every core change gets an independent audit before it reaches main

Any commit to `main` that changes core must pass an audit by a reviewer who didn't write it (a fresh agent with no
context from the change's session, or a human), and must record the result in a `Core-Audit:` commit trailer. Pushes
to `main` don't wait on CI, so the audit happens before you push; the nightly run (`.github/workflows/nightly.yml`)
flags any core commit of the day that lacks one, and pull requests are checked on the pull request.

1. Get the core diff: `.github/core-audit/core-diff.sh origin/main HEAD` (or `<sha>^ <sha>` for one commit).
2. Give the reviewer only that diff, this section, and the commit message. For each changed hunk it answers:
   - **Necessary:** could this live in `ext/` instead, using hooks core already has (upstream's own, or PocketOne's existing
     seams such as `OnFilesystemCreate`, `BaseAppConfig.AppMigrations`/`OnLog`, or the `ext/jsvm` engine's extension methods)?
   - **Minimal:** is it the smallest seam that works, with no feature logic, policy or extension-specific names in core?
   - **Sensible:** marked `// PocketOne:`, documented on the hook, upstream behavior unchanged when nothing uses it, tested?
   - **Listed:** a new or changed hook is on its `core-hook/<name>` branch (a new one is added to `HOOK_STACK`) and has
     its row in the README's "Changes we made to PocketBase" Hooks table (what it lets extensions do, which extensions
     use it, its diff); a bug fix has its `core-bugfix/<name>` branch and its row in the Bugfixes table.
3. Pass: add the trailer and push. Fail: move the logic to `ext/` or shrink the seam, then audit again.

Trailer forms (the last line of the commit message, after a blank line):

```
Core-Audit: pass - <one line: what the core change is and why it can't live in ext/>
Core-Audit: upstream-merge v0.x.y
```

`upstream-merge` is only for merging a PocketBase release; resolve conflicts in the `// PocketOne:` spots and audit any
PocketOne change the merge forced.

## Branches that show our core changes

Three kinds, all on upstream `master` (PocketBase's release line; `develop` is unstable and never a base) and code only
(no `_test.go` files), rebuilt and force-pushed (never merged) by `.github/core-audit/core-changes-branch.sh` after every
upstream pull and after a core change lands on main:

- `core-changes`: upstream `master` plus one commit holding every core change on `main` (no `ext/` or other
  PocketOne-owned paths), to audit all of it in one diff.
- `core-hook/<name>`: one hook each, stacked in the script's `HOOK_STACK` order (each on the one before), so its diff
  against the previous branch shows just that hook.
- `core-bugfix/<name>`: one upstream bug fix each, written for upstream (no `// PocketOne:` markers).

The script ends with a sum check: the hook stack plus every bugfix branch must match `core-changes` in code (comments
may differ), so a core change on `main` without its branch fails it. Run
`git fetch upstream origin && .github/core-audit/core-changes-branch.sh`, then
`git push --force origin core-changes 'refs/heads/core-hook/*' 'refs/heads/core-bugfix/*'`, and commit `README.md` if
the script repinned its compare links. Conflicts stop the script with the worktree left to resolve; resolutions are
remembered (rerere) for later runs.

The `upstream` remote is PocketHost's maintained mirror of PocketBase,
[`github.com/pocketonehq/pocketbase`](https://github.com/pocketonehq/pocketbase): every PocketBase branch and tag,
fast-forwarded once a day from `https://ganigeorgiev.com/git/pocketbase/pocketbase` by the mirror's own workflow, so
day-to-day fetches never reach PocketBase's server. Never fetch PocketBase's server from your own machine. Set it up once
per clone, without tags (PocketBase's `v0.x` tags must never land here, or Go would take them for PocketOne releases):

```
git remote add upstream https://github.com/pocketonehq/pocketbase.git
git config remote.upstream.tagopt --no-tags
```

A clone set up before 2026-11-16 (when `upstream` was this repo's own `upstream/*` branches) switches with
`git remote set-url upstream https://github.com/pocketonehq/pocketbase.git` and
`git config remote.upstream.fetch '+refs/heads/*:refs/remotes/upstream/*'`. To fetch one release without its tag:
`git fetch upstream refs/tags/vX.Y.Z`.

## Syncing with upstream

Merge `upstream/master` (a PocketBase release) into `main` on a side branch in its own worktree, never on `main`
directly, and review it before it lands:

1. `git worktree add -b upstream-sync/vX.Y.Z <dir> origin/main`, then `git merge --no-ff --no-commit upstream/master`.
   Resolve conflicts at the `// PocketOne:` spots. Then run `.github/upstream-changelog.sh`: `CHANGELOG.md` is ours, and
   it copies upstream's `CHANGELOG.md` into `CHANGELOG.pocketbase.md`.
2. Review what changed (`git log --oneline <old base>..upstream/master` and `CHANGELOG.pocketbase.md`): for each change,
   what it means for our hooks, extensions and contracts, and fix in `ext/` what it breaks.
3. Check each bug fix in "Core changes that aren't hooks": if upstream now fixes it, drop ours (and its
   `core-bugfix/<name>` branch and README row); if upstream changed the code around it, keep ours only for what is still
   broken.
4. `go mod tidy` in every module (upstream's dependency bumps reach them), build, vet, and run the full test suite.
5. Commit with `Core-Audit: upstream-merge vX.Y.Z`, add the **PocketBase** line to `CHANGELOG.md`, rebuild the
   core branches with `core-changes-branch.sh`, then merge the side branch into local `main` and push it.

## Core changes that aren't hooks

Bug fixes to upstream behavior, each tested and meant for upstream; drop each one once upstream has it:

- `apis/middlewares.go` `logRequest`: logs the status the client actually got for a plain error, not 0.
- `apis/realtime.go` `realtimeCanAccessRecord`: a superuser's subscription filter may use hidden fields, as in the
  list API (`TestRealtimeSuperuserFilterOnHiddenField`).
- `core/notify_watcher.go`: keeps the notify file until the next write, so macOS (kqueue) sees the change.
- `core/notify_watcher.go`: stops the watcher goroutine (and waits for it) before `ClearBootstrap`, which it raced
  (`TestNotifyWatcher_StoppedBeforeClearBootstrap`, `TestNotifyWatcher_QueuedReloadAfterClearBootstrap`).
- `tools/cron/cron.go` `Start`: the ticker goroutine keeps its own ticker, since `Stop` (buffered `tickerDone`
  since v0.42) no longer waits for it before setting `c.ticker` to nil (`TestCronStopDoesNotRaceTicker`).
- `apis/realtime.go` record and dry-cache broadcasts: messages go through `subscriptions.SendOrdered`
  (`tools/subscriptions/ordered.go`), one ordered queue per client, instead of a goroutine per message, which
  delivered them in any order (`TestRealtimeBroadcastKeepsOrderPerClient`, `TestSendOrdered`).
- `tools/subscriptions/client.go` `DefaultClient`: `Discard` ends pending sends and waits for them before it closes
  the channel, instead of closing it under them and recovering their panic (`TestDiscardWhileSending`).

## Writing an extension

Read [docs/contracts.md](docs/contracts.md) (the idea) and [docs/extensions.md](docs/extensions.md) (the code) first.
Extensions meet only through **contracts**: named, versioned Go interfaces in `ext/contracts/<id path>/vN/`, listed in
the catalogue [ext/contracts/README.md](ext/contracts/README.md). Design the contracts before the extension, in this
order of preference:

1. **Use an existing contract.** Search the catalogue first. If a contract already promises what your extension
   needs, put it in `Requires` or `WorksWith`. If one already promises what your extension offers, provide that one:
   your extension then stands in for the current provider (a default steps aside; otherwise `providers:` in
   `pocketone.yaml` picks).
2. **Extend an existing contract.** These leave its promise unchanged:
   - Provide it alongside others, for contracts that take a chain (`mail.fallback/v1`).
   - Decorate whatever provides it (`Contract.Wrap`, declared in `Wraps`).
   - Add or replace `pocketone` commands through `cli.commands/v1`.
   - Give it a dashboard screen (`Contract.UI`).

   If the promise itself has to change, add `vN+1` beside `vN`, reviewed with the contract's owners. Never change what
   a published `vN` promises.
3. **Invent a new contract only when none fits.** In the commit message, name the contracts you checked and say why
   none fits. Keep it small and name it for what it does (`restore/v1`), never for your extension. A new optional
   ability next to an existing contract is a small companion contract (`mail.suppression/v1` beside `bulkmail/v1`),
   not a second copy of the contract with changes. Add it with its README (front matter `owners`, `summary`; sections
   `Use`, `Provide`, `Extensions`, `License`), per
   [Add a contract](ext/contracts/README.md#add-a-contract). Contracts are reviewed like core: every provider and
   caller relies on the promise.

Rules every extension follows:

- **Every API a client can call is a contract operation** under `/api/_c/<id>/`. Extensions register no routes of their
  own; only paths a standard fixes stay at the root (`/.well-known/*`, `/mcp`).
- **Never import another extension's module** (tests and examples may). Anything shared, such as helpers, scopes or a
  client, belongs in the contract package.
- **Declare by contract id, never by extension name**, in `Contracts()` (`Implements`, `Defaults`, `Wraps`, `Requires`,
  `WorksWith`). Provide in `Register` (`Provide` / `ProvideDefault`); look contracts up only after registration
  (`Of`, `Must` in hooks or at serve time). A lookup inside `Register` panics.
- **After changing a contract interface**, run `go generate ./...` in `ext/contracts`. Then run
  `go run ./.github/extensions contracts` to regenerate the catalogue and each contract's Extensions section, and
  `go run ./.github/extensions check`. The catalogue and Extensions sections are generated files (`.github/regenerate.sh`,
  see below).

## Other rules

- Extensions are their own Go modules under `ext/<name>/` with a README and a minimal `example/main.go`
  (`docs/extensions.md`). Modules that reach another `ext/` module add a `replace` for it.
- Extension CI (`.github/workflows/extensions.yml`, tool in `.github/extensions/`): every `extkit.Extension` module has a
  conformance test, and each module a change affects builds, vets and passes `go test -race`. Run what CI runs on a
  pull request with `go run ./.github/extensions test` (or `test ext/<name>`).
- **An extension is self-contained:** its catalogue entry (`title`, `group`, `summary`) is in its README's front
  matter beside `owners` and `version`, its changelog is `ext/<name>/CHANGELOG.md`, and work on it that waits for something
else to exist first goes in an optional `ext/<name>/TODO.md`, each item under a `**Waits on:**` line naming what it
waits for (not published on pocketone.dev). `site/extensions.json` holds
  only the catalogue groups and the few folders without a page (`links`). A change to an extension touches only its
  folder.
- **Generated files land on `main` with the change that produces them.** pocketone.dev's pages (`site/`), nav,
  search index, both sites' `llms.txt` / `llms-full.txt`, the Agent Skills' `reference/` folders and the contract
  indexes are built by `.github/regenerate.sh`. Every clone turns on the repo's hooks once with
  `git config core.hooksPath .github/hooks`: on `main` only, the `pre-commit` and `pre-merge-commit` hooks run
  `regenerate.sh` on the staged files and refuse a commit that would leave any generated file stale (about 3 s). When
  one is refused, run `.github/regenerate.sh`, `git add` the files it lists and commit again. So `main` never carries
  stale generated files. Commits on other branches skip the check and leave generated files alone, so a branch
  touches only its own files; its generated files go in its merge commit into `main`. Outside pull requests are the exception: their
  authors never commit generated files (they only check that the site builds, `cd site/sync && go run . --validate`),
  and CI regenerates on `main` after the merge and commits as github-actions[bot].
- **Screenshots** in the docs are listed in `site/screenshots.json` (what each shows, the pages that use it, the feature
  it depends on, how to retake it); site/sync fails when the list and the docs disagree. A change to a screen or a
  starter kit that a screenshot shows retakes that screenshot in the same commit.
- Agent Skills (`agent/`, listed in `.claude-plugin/marketplace.json`): `site/sync` also writes each skill's `reference/`
  folder and both sites' `llms.txt` / `llms-full.txt` from the docs pages (regenerated on `main`, as above). The `SKILL.md` files are hand-written: a change
  that makes one wrong (a command, a `pocketone.yaml` key, an extension, a Pages API) fixes it in the same commit.
- JS types: after a core change that touches the JS API (a hook, a type JS sees) or an edit to `ext/jsvm/pocketone.d.ts`,
  run `go generate` in `ext/jsvm` to rebuild `ext/jsvm/types.d.ts` (its `TestTypes` tests fail until you do). An
  extension with a JS API declares it in a `TypeScript()` method.
- Environment variables are named for the layer that reads them: `PB_` is PocketBase core's (leave those alone),
  `P1_` is PocketOne's in every build (`extkit.EnvVar("TOKEN")` is `P1_TOKEN`), and a build on top uses its own
  prefix for its own variables. Never name a variable after the binary.
- **Trust contracts; guard edges.** Be defensive only where data can really be wrong: request bodies, imported files,
  settings a person typed, what a model writes back, the network. Trust what an API documents (a contract's result, a
  core type, `ctx.auth` being a record or `null`): no `typeof` checks, no `|| []` around a result that's always a list,
  no `try`/`catch` that turns an error into an empty answer. A guard that can't fire misleads the reader, and a
  swallowed error hides a bug. Unsure what an API returns? Read its docs or types.
- Build checks: `go build -o /dev/null ./...` (a plain `go build` in a single-main module writes a binary into the
  tree). Stage files by name; never commit built binaries.
- **Changelog:** every commit to `main` with a user-visible change adds its line under `## Unreleased`, in the same
  commit: in the extension's own `ext/<name>/CHANGELOG.md` for a change to one extension, otherwise in the root
  [CHANGELOG.md](CHANGELOG.md), PocketOne's (policy at its top; breaking changes need an **Upgrading** step).
  Extension versions go up only at a release: `go run ./.github/extensions release vX.Y.Z` raises each extension with
  unreleased lines (minor with upgrade steps, else patch), stamps every changelog, and lists the new versions in the
  release's section, which is its notes (`examples/pocketone/release-notes.sh vX.Y.Z`). Upstream PocketBase's changelog
  is `CHANGELOG.pocketbase.md`; never edit it by hand (`.github/upstream-changelog.sh` refreshes it).
- **Pushing to `main` waits on nothing.** PocketOne is in alpha: merge your branch into local `main` (the pre-commit
  hook regenerates generated files) and `git push origin main`. No CI gates the push. `go run ./.github/extensions quick`
  (the rules, `go mod tidy -diff` and `go vet` in each affected module, about 20 s) is there when you want a fast check
  first; nothing runs it for you. CI checks `main` out of band: `.github/workflows/nightly.yml` runs once a day when
  `main` moved (or on demand, `gh workflow run nightly.yml --ref main`): the extension rules and every module's tidy,
  build, vet and `-race` tests, Core-Audit trailers on the day's commits, and a check that generated files are current.
  A red nightly is a bug to fix on `main` next. Outside pull requests get the checks on the pull request.
- **Tests, by when they run.** Before pushing: build and vet what you touched, and run the tests of the packages you
  changed when they're quick; no full suite locally. The nightly run tests every module on `main`. A test that fails
  there but not locally is a real bug to fix (often Linux-only), not something to re-run until it passes. The full suite runs once per release: `examples/pocketone/release.sh --check` starts
  `.github/workflows/release-check.yml` on `main` (every module with `-race`, PocketBase's suite on our core, `ext/jsvm`
  on 32-bit ARM, every release target), and `release.sh vX.Y.Z` tags only a commit that passed it.
- Plans and internal notes go in a private maintainer repo, never here.
