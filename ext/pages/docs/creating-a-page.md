---
title: Creating a Page
description: Create .ets and .md files in pb_pages to add pages, with index files and nested folders mapping to URLs.
---

# Creating a Page

A **page** is a template file, `.ets` or `.md`, anywhere in `pb_pages/`. Its path becomes its URL. (A `.tsx` component works too; see [File types](file-types.md).)

## Creating pages

1. **Create a file**: inside `pb_pages/`, create an `.ets` file. For example, an "About" page:

```text
pb_pages/about.ets  -> serves "/about"
```

2. **`index` files**: `index` maps to its folder.

```text
pb_pages/index.ets           -> serves "/"
pb_pages/products/index.ets  -> serves "/products" (and "/products/")
```

3. **Nested pages**: folders become URL segments.

```text
pb_pages/
├── index.ets         -> "/"
├── about.ets         -> "/about"
├── contact.ets       -> "/contact"
└── products/
    ├── index.ets     -> "/products"
    └── details.ets   -> "/products/details"
```

4. **Markdown pages**: a `.md` file works the same way. Its tags run first (plain JavaScript), then Markdown. See [Markdown](markdown.md).

```text
pb_pages/docs/routing.md  -> "/docs/routing"
```

The extension is always dropped from the URL. To add data or handle forms, put a `.ts` file with the same name next to the page (`about.ts` beside `about.ets`); see [Loading Data](loading-data.md).
