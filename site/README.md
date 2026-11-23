# pocketone.dev

The PocketOne website: what PocketOne is, how to install it, and its extensions. A PocketOne app (`ext/pages`),
started from the pocketpages.dev site, whose docs now live in [`../ext/pages/docs`](../ext/pages/docs) and appear here
under `/docs/extensions/pages`.

## Layout

- `pb_pages/` is the app: the root layout, the home page in `(splash)/`, the docs in `(main)/docs/` and the blog in
  `(main)/blog/`.
- `pb_pages/(main)/_private/toc.md` is the docs navigation, hand-written. Its Extend PocketOne section slides into the
  Contracts and Extensions navs (`toc-contracts.md`, `toc-extensions.md`, written by `sync/`); `_private/docs-nav.js`
  picks which nav a page shows.
- `pb_pages/(main)/blog/`: every `.md` file there is a post (front matter: title, description, date, author, tags).
- `pb_pages/assets/site.css` is the whole stylesheet: hand-written, no build step.
- Code tabs (`pb_pages/assets/docs.js`): consecutive ```` ```ts ````, ```` ```go ```` and ```` ```ets ```` blocks on a page
  become one tabbed example (TS | Go | ETS); TS and ETS are one preference against Go and never share a group; the reader's language is kept in
  `localStorage` (`code-lang`) and applies to every group. Extension docs (`ext/*/docs`) use them (examples are
  TypeScript page code, not hooks or JS); READMEs keep samples inline (text between languages).
- `design/`: the logo and mascot (the joey in the pocket). `mark.svg` is the source; `design/render.sh` renders
  `logo.html` to PNGs in `design/out/` and copies the mark and favicons into `pb_pages/`.

Core PocketBase topics are linked to pocketbase.io, never copied here.

## Extension docs (`sync/`)

Extension pages are copies of the READMEs in `ext/`, made by `sync/`. Don't edit them here; edit the README and run:

```bash
cd site/sync && go run .
```

- Each extension's catalogue entry (`title`, `group`, one-line `summary`) is in its README's front matter.
  `extensions.json` holds the groups (and `links`: the few folders with no page of their own). Within a group,
  extensions are sorted by title.
- `go run . --validate` (pull requests) only checks the pages build; on `main`, CI runs `.github/regenerate.sh` and
  commits the generated files, so a pull request never has to.
- Each `ext/<name>/README.md` becomes `/docs/extensions/<name>`, with the extension's `pocketone.yaml` keys (from
  `pocketone config --json`, so it builds `examples/pocketone`) added to its `## Configure` section. Other `.md` files
  in the extension folder (`CHANGELOG.md` aside) become guides under it; `docs/extensions.md` becomes `/docs/writing-an-extension`.
- An extension's `site/` folder (pages, see `extsite.go` and
  [Writing an extension](../docs/extensions.md#what-an-extension-folder-contains)) is copied under
  `/docs/extensions/<name>/`, with its relative links pointed at site URLs and `source:` added for the edit link. Its
  `site/_private/toc.md` becomes `(main)/_private/toc-<name>.md`, and a `site/index.md` replaces the README as the
  landing page (the README moves to `/docs/extensions/<name>/extension`). The nav file `toc-<name>.md` is the sidebar of every page under
  `/docs/extensions/<name>` (`_private/docs-nav.js`, loaded with `ctx.resolve` so a deploy takes effect without a restart, picks it; `_private/site-navs.js` lists
  them). A nav link into or out of such a site loads the whole page (`assets/docs.js`), so the sidebar changes. Its `blog/*.md` posts are copied into
  `(main)/blog/` (only files sync wrote are ever removed there).
- It also writes `/docs/config-reference`: every `pocketone.yaml` section.
- Links between extensions and docs point at the site pages; links to source files point at GitHub.
- The old URLs from before plugins were called extensions (`/docs/plugins/**`, `/docs/writing-a-plugin`) answer 301
  redirects to the new ones, from the hand-written endpoints `pb_pages/(main)/docs/plugins.ts`, `plugins/[name].ts`,
  `plugins/[name]/[guide].ts` and `writing-a-plugin.ts`.
- It also writes the extension index (`/docs/extensions`) and the extension part of the docs nav (between the markers in
  `pb_pages/(main)/_private/toc.md`).
- CI (`.github/workflows/site.yml`) runs `go test .` and `go run . --check`, and fails when a page is out of date.

## Ask and comments

- **Ask** (`pb_pages/assets/ask.js`, `pb_pages/ask.ts`, `pb_pages/_private/ask.ts`): the header button, `/` or
  Cmd/Ctrl-K opens a box that answers a question from the docs, citing the pages it used. The pages come from an LLM
  Search bucket, `docs`, holding the site's own docs folder (`$llm.search.addFolder` on `pb_pages/(main)/docs`, read
  again every five minutes, each result linked to its page and section). The answer is one `$llm` call per question,
  for the visitor (`$llm.for`), with `ASK_MODEL` (see the file for the default). Answers show code in the reader's
  language, the one the docs' TS | Go tabs remember (TypeScript unless they picked Go): the passages lose their code
  blocks in the other language before the model sees them. It needs an AI provider with text and
  embedding models in **Settings → LLM** and a limit for **All guests together** (guest calls are refused without
  one). A limit used up still leaves the matching pages listed. The same question asked again within the hour gets
  the stored answer. Run locally with `--dir ./pb_data` (the docs folder is found from the app's folder, the one
  holding `pb_data`).
- **Comments** (`pb_pages/_private/docs-footer.js`, `pb_pages/assets/docs.js`): each docs page ends with a
  [giscus](https://giscus.app) box backed by GitHub Discussions (the Q&A category of pocketonehq/pocketone). A page's
  thread is found by an exact term, not its URL: `pocketone.dev/docs/plugins/<name>` (the pre-rename URL, so old threads follow) for an extension and its guides,
  `pocketone.dev/<path>` for other pages. Front matter `discussion: <term>` picks another term (keep it when a page
  moves, so its thread follows); `discussion: false` turns comments off. The docs home and the extension index have none.
  A thread is created on the first comment or reaction.
- Docs links swap only the page body (Datastar, scoped to the nav). A swap gets the page without its layouts, so
  `docs-footer.js` sends the footer (edit link, comments, tab title) along with it.

## Run it

With a `pocketone` binary, from this folder:

```bash
pocketone superuser upsert admin@example.com password --dir ./pb_data
pocketone serve --dev --dir ./pb_data
```

Pages finds `pb_pages` next to `pb_data`. Open http://localhost:8090.

## Deploy

Pushes to `main` deploy `site/pb_pages` to pocketone.dev once the sync check passes (`.github/workflows/site.yml`,
job `deploy`). It needs the `POCKETHOST_TOKEN` repository secret; without it the job skips. To deploy by hand:
`cd site && npx pockethost@latest deploy pocketone --no-node-modules`.
