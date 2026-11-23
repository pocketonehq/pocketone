// The Search screen for the PocketBase dashboard, for the llm.search/v1 contract (PocketOne ext/contracts/llm/search/v1). A
// sidebar leads to: Chat (RAG: questions answered from chosen buckets, with the passages they came from), Search (the raw
// hybrid search results), each bucket (its sources: files, pasted text, folders, zips, web pages, Git repositories,
// collections, and their documents), and Settings (the app's embedding model and embedding progress). It calls
// llm.search/v1, and llm.text/v1 and llm.embed/v1 for Ask and the model pickers, so it works whoever provides them.
// Served at /_/extensions/search/main.js.
(() => {
    const api = "/api/_c/llm.search/v1";
    const send = (path, body) =>
        app.pb.send(api + path, body !== undefined ? { method: "POST", body, requestKey: null } : { requestKey: null });
    const errText = (err) => err?.response?.message || err?.message || String(err);
    const MAX_FILE = 10 << 20;

    app.store.headerLinks = [
        ...app.store.headerLinks.filter((l) => l.href !== "#/settings"),
        { href: "#/search", icon: "ri-search-eye-line", label: "Search" },
        ...app.store.headerLinks.filter((l) => l.href === "#/settings"),
    ];

    const style = document.createElement("style");
    style.textContent = `
        .search-dropzone { border: 2px dashed var(--borderColor); border-radius: var(--baseRadius); padding: var(--smSpacing);
            text-align: center; color: var(--txtHintColor); transition: border-color 0.15s, background 0.15s; }
        .search-dropzone.over { border-color: var(--primaryColor); background: var(--baseAlt1Color); }
        .search-table { width: 100%; border-collapse: collapse; }
        .search-table th, .search-table td { text-align: left; padding: 6px 10px; border-bottom: 1px solid var(--baseAlt2Color); vertical-align: top; }
        .search-table th { font-weight: 600; color: var(--txtHintColor); font-size: var(--smFontSize); }
        .search-table tr.link { cursor: pointer; } .search-table tr.link:hover td { background: var(--baseAlt1Color); }
        .search-result { border: 1px solid var(--baseAlt2Color); border-radius: var(--baseRadius); padding: 10px; margin-bottom: 8px; }
        .search-result pre { white-space: pre-wrap; margin: 6px 0 0; max-height: 240px; overflow: auto; font-size: var(--smFontSize); }
        .search-num { text-align: right !important; font-variant-numeric: tabular-nums; white-space: nowrap; }
        .search-dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; margin-left: auto; flex-shrink: 0; }
        .search-chip { cursor: pointer; }
        .search-turn { margin-bottom: 18px; }
        .search-q { font-weight: 600; margin-bottom: 6px; }
        .search-a { line-height: 1.55; } .search-a pre { white-space: pre-wrap; } .search-a code { font-size: 0.92em; }
        .search-passages { margin-top: 8px; }
        .search-rows td { vertical-align: middle; }
        .search-rows .name { max-width: 0; width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
        .search-rows .name a { color: inherit; text-decoration: none; } .search-rows .name a:hover { text-decoration: underline; }
        .search-rows .icons { white-space: nowrap; text-align: right; }
        .search-rows .icons .btn { margin-left: 2px; }
        .search-status-icon { font-size: 1.2em; vertical-align: middle; }
        .search-totals { color: var(--txtHintColor); font-size: var(--smFontSize); margin-top: 8px; }
        .search-sources { display: flex; flex-direction: column; gap: 10px; margin-bottom: var(--baseSpacing); }
        .search-source { border: 1px solid var(--baseAlt2Color); border-radius: var(--baseRadius); padding: 12px 14px; }
        .search-source-head { display: flex; align-items: flex-start; gap: 10px; flex-wrap: wrap; }
        .search-source-icon { font-size: 1.35em; color: var(--txtHintColor); line-height: 1.2; }
        .search-source-name { font-weight: 600; word-break: break-all; }
        .search-source-meta { color: var(--txtHintColor); font-size: var(--smFontSize); margin-top: 2px; }
        .search-source-actions { display: flex; gap: 6px; margin-left: auto; flex-shrink: 0; }
        .search-source-status { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-top: 8px; font-size: var(--smFontSize); }
        .search-source-docs { margin-top: 8px; overflow-x: auto; }
        .search-passages summary { cursor: pointer; color: var(--txtHintColor); font-size: var(--smFontSize); }`;
    document.head.append(style);

    const fmtBytes = (n) => (n >= 1 << 20 ? (n / (1 << 20)).toFixed(1) + " MB" : n >= 1024 ? Math.round(n / 1024) + " KB" : n + " B");
    const fmtCost = (micro) => (micro ? "$" + (micro / 1e6).toFixed(micro < 10000 ? 4 : 2) : "");
    const statusLabel = (s, err) => t.span({
        className: `label ${s === "ready" ? "success" : s === "error" ? "danger" : "warning"}`,
        title: err || null,
    }, s === "queued" ? "waiting" : s === "embedding" ? "embedding" : s);
    const recordHref = (r) => `#/collections?collection=${encodeURIComponent(r.collection)}&recordId=${encodeURIComponent(r.id)}`;

    // embedModels is llm.embed/v1's list: [{ id: "provider/model", name, prices }]
    async function embedModels() {
        try {
            return (await app.pb.send("/api/_c/llm.embed/v1/models", { requestKey: null })).models;
        } catch (_) {
            return [];
        }
    }
    const modelLabel = (m) => {
        const p = (m.prices || []).find((x) => /token/i.test(x.unit));
        return (m.name || m.id) + (p ? ` ($${p.usd} per ${p.unit})` : "");
    };

    // nav is the sidebar's bucket list, shared by every Search page
    const nav = store({ buckets: [] });
    async function refreshNav() {
        nav.buckets = (await send("/buckets")).buckets;
    }
    const bucketDot = (b) => b.errors ? "var(--dangerColor)" : b.pending ? "var(--warningColor)" : b.documents ? "var(--successColor)" : "var(--txtDisabledColor)";
    const bucketHref = (n) => "#/search/b/" + encodeURIComponent(n);

    function sidebar() {
        const item = (href, icon, label, extra) => t.a({
            href, className: () => `nav-item ${app.utils.isActivePath(href, false) ? "active" : ""}`,
        }, t.i({ className: icon, ariaHidden: true }), t.span({ className: "txt" }, label), extra || null);
        return app.components.pageSidebar({ className: "settings-sidebar" },
            t.nav({ className: "sidebar-content scrollable" }, () => [
                t.details({ className: "nav-group", open: true },
                    t.summary({ tabIndex: -1, onclick: () => false }, "Search"),
                    item("#/search/chat", "ri-sparkling-2-line", "Chat"),
                    item("#/search/find", "ri-search-line", "Search"),
                    item("#/search/settings", "ri-settings-3-line", "Settings")),
                t.details({ className: "nav-group", open: true },
                    t.summary({ tabIndex: -1, onclick: () => false }, "Buckets"),
                    item("#/search", "ri-stack-line", "All buckets"),
                    ...nav.buckets.map((b) => item(bucketHref(b.name), "ri-inbox-archive-line", b.title || b.name,
                        t.span({ className: "search-dot", style: `background: ${bucketDot(b)}` }))),
                    item("#/search?new=1", "ri-add-line", "New bucket")),
            ]));
    }

    // stateIcon is a status as one icon, its words in the tooltip
    const stateIcon = (cls, tip) => t.i({ className: "search-status-icon " + cls, ariaLabel: app.attrs.tooltip(tip) });

    // showModal mounts a modal (app.modals wants it in the page) and opens it; it's removed again once closed
    function showModal(el) {
        document.body.appendChild(el);
        app.modals.open(el);
    }

    function page(title, crumbs, body, actions) {
        app.store.title = title;
        refreshNav();
        return t.div({ className: "page page-search" },
            sidebar(),
            t.div({ className: "page-content full-height" },
                t.header({ className: "page-header flex-nowrap" }, t.nav({ className: "breadcrumbs" }, ...crumbs),
                    actions?.secondary ? t.div({ className: "page-header-secondary-btns" }, ...actions.secondary) : null,
                    actions?.primary ? t.div({ className: "page-header-primary-btns" }, ...actions.primary) : null),
                t.div({ className: "wrapper m-b-base" }, body),
                t.footer({ className: "page-footer" }, app.components.credits())));
    }
    const crumb = (label, href) => href ? t.a({ className: "breadcrumb-item", href }, label) : t.div({ className: "breadcrumb-item" }, label);

    // noModel is the notice shown while there's no embedding model
    const noModel = () => t.div({ className: "alert warning m-b-sm" }, t.div({ className: "content" },
        "Nothing can be searched or embedded yet: add an AI provider with embedding models in ",
        t.a({ href: "#/settings/llm" }, "Settings → LLM"), ". What you add meanwhile waits, and is embedded as soon as there is one."));

    // bucketPicker lets the viewer choose buckets as chips; state.picked is the chosen names (none: every bucket)
    function bucketPicker(state) {
        const on = (n) => state.picked.includes(n);
        const chip = (label, active, onclick, title) => t.button({ type: "button", title: title || null,
            className: () => `btn xs search-chip ${active() ? "" : "secondary"}`, onclick }, label);
        return t.div({ className: "flex gap-sm m-b-sm", style: "flex-wrap: wrap; align-items: center" },
            t.span({ className: "txt-hint txt-sm" }, "In"),
            () => [
                chip("All buckets", () => !state.picked.length, () => (state.picked = [])),
                ...nav.buckets.map((b) => chip(b.title || b.name, () => on(b.name),
                    () => (state.picked = on(b.name) ? state.picked.filter((x) => x !== b.name) : [...state.picked, b.name]), b.name)),
            ]);
    }
    // q is a query parameter (the router gives each as a list)
    const q = (route, key) => [].concat(route.query?.[key] ?? [])[0] || "";
    const pickedFrom = (route) => q(route, "buckets").split(",").map((x) => x.trim()).filter(Boolean);

    // results draws a search's matches
    function results(r) {
        if (!r) return null;
        if (!r.results.length) return t.div({ className: "txt-hint" }, "No matches.");
        return t.div(null,
            t.div({ className: "txt-hint txt-sm m-b-sm" }, `Hybrid search (semantic with ${r.model}, plus keywords)` +
                (r.usage?.costMicroUSD ? `; the query cost ${fmtCost(r.usage.costMicroUSD)}` : "") + "."),
            ...r.results.map((x) => t.div({ className: "search-result" },
                t.div({ className: "flex gap-sm" },
                    t.a({ className: "label", href: bucketHref(x.bucket) }, x.bucket),
                    x.source && x.source !== x.document ? t.span({ className: "txt-hint" }, x.source + " ›") : null,
                    x.record ? t.a({ href: recordHref(x.record) }, t.strong(null, x.document)) : t.strong(null, x.document),
                    x.heading ? t.span({ className: "txt-hint" }, "› " + x.heading) : null,
                    t.span({ className: "txt-hint txt-sm" }, `lines ${x.startLine}–${x.endLine}`),
                    t.span({ className: "m-l-auto txt-hint txt-sm" }, x.score.toFixed(4))),
                t.pre(null, x.text))));
    }

    // ---- all buckets

    app.routes.superuserOnly("#/search", (route) => {
        const data = store({ loading: true, buckets: [], status: null, creating: !!q(route, "new"), name: "", title: "", description: "" });

        async function load() {
            try {
                const [b, st] = await Promise.all([send("/buckets"), send("/status")]);
                data.buckets = b.buckets;
                data.status = st;
            } catch (err) {
                app.checkApiError(err);
            }
            data.loading = false;
        }

        async function create(e) {
            e.preventDefault();
            try {
                const b = await send("/ensure", { name: data.name.trim(), title: data.title.trim(), description: data.description.trim() });
                app.modals.close();
                window.location.hash = bucketHref(b.name);
            } catch (err) {
                app.toasts.error(errText(err));
            }
        }

        load();

        return page("Search", [crumb("Search"), crumb("All buckets")], () => {
            if (data.loading) return t.div({ className: "block txt-center" }, t.span({ className: "loader lg" }));
            return t.div(null,
                data.status && !data.status.model ? noModel() : null,
                data.buckets.length
                    ? t.div({ style: "overflow-x: auto" }, t.table({ className: "search-table search-rows m-b-base" },
                        t.thead(null, t.tr(null, t.th(null, "Bucket"), t.th(null, "Made by"), t.th({ className: "search-num" }, "Sources"),
                            t.th({ className: "search-num" }, "Documents"), t.th({ className: "search-num" }, "Size"), t.th(null, ""))),
                        t.tbody(null, ...data.buckets.map((b) => t.tr({ className: "link", onclick: () => (window.location.hash = bucketHref(b.name)) },
                            t.td({ className: "name" }, t.strong(null, b.title || b.name),
                                b.title && b.title !== b.name ? t.span({ className: "txt-hint m-l-5" }, b.name) : null,
                                b.description ? t.i({ className: "ri-information-line txt-hint m-l-5", ariaLabel: app.attrs.tooltip(b.description) }) : null),
                            t.td({ className: "txt-hint", style: "white-space: nowrap" }, b.owner || ""),
                            t.td({ className: "search-num txt-hint" }, b.sources),
                            t.td({ className: "search-num txt-hint" }, b.documents),
                            t.td({ className: "search-num txt-hint" }, fmtBytes(b.bytes)),
                            t.td({ className: "txt-center" }, b.documents === 0 ? stateIcon("ri-checkbox-blank-circle-line txt-hint", "Empty: open it to add something")
                                : b.errors ? stateIcon("ri-error-warning-fill txt-danger", `${b.errors} failed: open it to see why`)
                                    : b.pending ? stateIcon("ri-time-line txt-warning", `${b.pending} document${b.pending === 1 ? "" : "s"} waiting to be embedded`)
                                        : stateIcon("ri-checkbox-circle-fill txt-success", "Ready to search")))))))
                    : t.div({ className: "block txt-center txt-hint p-base" },
                        "No buckets yet. A bucket is a knowledge base your app searches and grounds its AI answers in (RAG). ",
                        t.a({ href: "#", onclick: (e) => { e.preventDefault(); openNew(); } }, "Make one"), "."),
            );
        }, { secondary: [
            t.a({ className: "btn circle transparent secondary", href: "#/search/find", ariaLabel: app.attrs.tooltip("Search every bucket") },
                t.i({ className: "ri-search-line", ariaHidden: true })),
            t.a({ className: "btn circle transparent secondary", href: "#/search/chat", ariaLabel: app.attrs.tooltip("Chat with your buckets") },
                t.i({ className: "ri-sparkling-2-line", ariaHidden: true }))],
        primary: [
            t.button({ type: "button", className: "btn", onclick: () => openNew() },
                t.i({ className: "ri-add-line", ariaHidden: true }), t.span({ className: "txt" }, "New bucket"))] });

        function openNew() {
            data.name = data.title = data.description = "";
            showModal(t.div({ className: "modal popup", onafterclose: (el) => el.remove() },
                t.header({ className: "modal-header" }, t.h5({ className: "m-auto" }, "New bucket")),
                t.div({ className: "modal-content" }, newForm())));
        }

        function newForm() {
            return t.form({ className: "grid", onsubmit: create },
                        t.div({ className: "col-lg-4" }, t.div({ className: "field" }, t.label({}, "Name"),
                            t.input({ type: "text", required: true, pattern: "[A-Za-z0-9][A-Za-z0-9_\\-]{0,63}", placeholder: "docs",
                                value: () => data.name, oninput: (e) => (data.name = e.target.value) }))),
                        t.div({ className: "col-lg-8" }, t.div({ className: "field" }, t.label({}, "Title"),
                            t.input({ type: "text", value: () => data.title, oninput: (e) => (data.title = e.target.value) }))),
                        t.div({ className: "col-lg-12" }, t.div({ className: "field" }, t.label({}, "What belongs in it"),
                            t.input({ type: "text", value: () => data.description, oninput: (e) => (data.description = e.target.value) }))),
                        t.div({ className: "col-lg-12 flex" }, t.div({ className: "m-r-auto" }),
                            t.button({ type: "button", className: "btn transparent secondary", onclick: () => app.modals.close() }, t.span({ className: "txt" }, "Cancel")),
                            t.button({ className: "btn" }, t.span({ className: "txt" }, "Create bucket"))));
        }
    });

    // ---- search: the matching passages

    app.routes.superuserOnly("#/search/find", (route) => {
        const data = store({ picked: pickedFrom(route), query: q(route, "q"), searching: false, results: null, status: null });
        send("/status").then((st) => (data.status = st)).catch(() => {});

        async function run(e) {
            e?.preventDefault();
            if (!data.query.trim()) return;
            data.searching = true;
            try {
                data.results = await send("/search", { buckets: data.picked, query: data.query, limit: 10 });
            } catch (err) {
                data.results = null;
                app.toasts.error(errText(err));
            }
            data.searching = false;
        }
        if (data.query) run();

        return page("Search", [crumb("Search"), crumb("Hybrid search")], () => t.div(null,
            t.p({ className: "m-t-0 txt-hint" }, "Hybrid search: the passages that best match, ranked by meaning (semantic search with your embedding model) and by words (keyword search, a local index) together. This is what ",
                t.code(null, "$llm.search"), " and ", t.code(null, "enrich"), " get."),
            data.status && !data.status.model ? noModel() : null,
            bucketPicker(data),
            t.form({ className: "flex gap-sm m-b-sm", onsubmit: run },
                t.input({ type: "text", placeholder: "A question or a few words", style: "flex: 1", autofocus: true,
                    value: () => data.query, oninput: (e) => (data.query = e.target.value) }),
                t.button({ className: () => `btn ${data.searching ? "loading" : ""}` }, t.span({ className: "txt" }, "Search"))),
            () => results(data.results)));
    });

    // ---- chat: RAG over the buckets

    // textModels is llm.text/v1's list
    async function textModels() {
        try {
            return (await app.pb.send("/api/_c/llm.text/v1/models", { requestKey: null })).models;
        } catch (_) {
            return [];
        }
    }
    const remembered = (key, fallback) => { try { return localStorage.getItem(key) || fallback; } catch (_) { return fallback; } };
    const remember = (key, v) => { try { localStorage.setItem(key, v); } catch (_) {} };

    // streamText calls llm.text/v1's stream operation and hands each piece to onText; it resolves to the final result
    async function streamText(body, onText, signal) {
        const res = await fetch(app.pb.buildURL("/api/_c/llm.text/v1/stream"), {
            method: "POST", signal, body: JSON.stringify(body),
            headers: { Authorization: app.pb.authStore.token, "Content-Type": "application/json", Accept: "text/event-stream" },
        });
        if (!res.ok) {
            const b = await res.json().catch(() => ({}));
            throw new Error(b.message || res.statusText);
        }
        const reader = res.body.getReader();
        const dec = new TextDecoder();
        let buf = "", result = null;
        for (;;) {
            const { value, done } = await reader.read();
            if (done) break;
            buf += dec.decode(value, { stream: true });
            let i;
            while ((i = buf.indexOf("\n\n")) >= 0) {
                const block = buf.slice(0, i);
                buf = buf.slice(i + 2);
                let event = "", data = "";
                for (const line of block.split("\n")) {
                    if (line.startsWith("event:")) event = line.slice(6).trim();
                    else if (line.startsWith("data:")) data += line.slice(5).trim();
                }
                if (!data) continue;
                const msg = JSON.parse(data);
                if (event === "error") throw new Error(msg.message || "the model failed");
                if (msg.done) result = msg.result;
                else if (msg.text) onText(msg.text);
            }
        }
        return result;
    }

    // markdown turns a model's answer into safe HTML: everything escaped first, then headings, lists, code, bold,
    // italics and [n] citations
    function markdown(src) {
        const esc = (x) => x.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
        const inline = (x) => esc(x)
            .replace(/`([^`]+)`/g, "<code>$1</code>")
            .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>")
            .replace(/(^|[^*])\*([^*\s][^*]*)\*/g, "$1<em>$2</em>")
            .replace(/\[(\d+)\]/g, '<span class="label sm">$1</span>');
        const out = [];
        let list = null, code = null;
        const close = () => { if (list) { out.push(`</${list}>`); list = null; } };
        for (const line of src.split("\n")) {
            if (code !== null) {
                if (line.trim().startsWith("```")) { out.push("<pre>" + esc(code.join("\n")) + "</pre>"); code = null; } else code.push(line);
                continue;
            }
            if (line.trim().startsWith("```")) { close(); code = []; continue; }
            let m;
            if ((m = line.match(/^(#{1,6})\s+(.*)/))) { close(); out.push(`<div class="txt-bold m-t-sm">${inline(m[2])}</div>`); continue; }
            if ((m = line.match(/^\s*[-*]\s+(.*)/)) || (m = line.match(/^\s*\d+[.)]\s+(.*)/))) {
                const tag = /^\s*\d/.test(line) ? "ol" : "ul";
                if (list !== tag) { close(); out.push(`<${tag} style="margin: 4px 0 4px 20px">`); list = tag; }
                out.push(`<li>${inline(m[1])}</li>`);
                continue;
            }
            close();
            out.push(line.trim() ? `<div>${inline(line)}</div>` : '<div style="height: 8px"></div>');
        }
        close();
        if (code !== null) out.push("<pre>" + esc(code.join("\n")) + "</pre>");
        return out.join("");
    }

    const askSystem = "You answer questions about the user's own content, using the numbered passages given after each question. " +
        "Cite the passages you use by number, like [1]. If the passages don't answer the question, say so plainly instead of guessing.";

    app.routes.superuserOnly("#/search/chat", (route) => {
        const data = store({ picked: pickedFrom(route), models: [], model: "", question: "", turns: [], asking: false, status: null });
        let abort = null;
        send("/status").then((st) => (data.status = st)).catch(() => {});
        textModels().then((m) => {
            data.models = m;
            const saved = remembered("pocketone.search.askModel", "");
            data.model = m.some((x) => x.id === saved) ? saved : m[0]?.id || "";
        });

        async function ask(e) {
            e?.preventDefault();
            const q = data.question.trim();
            if (!q || data.asking) return;
            if (!data.model) return app.toasts.error("Add an AI provider with text models in Settings → LLM.");
            const history = data.turns.filter((x) => x.answer && !x.error).flatMap((x) => [{ role: "user", content: x.question }, { role: "assistant", content: x.answer }]);
            const turn = { question: q, answer: "", passages: [], error: "", cost: 0 };
            data.turns = [...data.turns, turn];
            const at = data.turns.length - 1;
            const set = (changes) => (data.turns = data.turns.map((x, j) => (j === at ? { ...x, ...changes } : x)));
            data.question = "";
            data.asking = true;
            abort = new AbortController();
            let text = "";
            try {
                const result = await streamText({
                    model: data.model, system: askSystem, messages: [...history, { role: "user", content: q }],
                    enrich: { buckets: data.picked.length ? data.picked : nav.buckets.map((b) => b.name), limit: 6 },
                }, (piece) => { text += piece; set({ answer: text }); }, abort.signal);
                set({ answer: result?.text || text, passages: result?.passages || [], cost: result?.usage?.costMicroUSD || 0 });
            } catch (err) {
                if (err.name !== "AbortError") set({ error: errText(err) });
            }
            data.asking = false;
            abort = null;
        }

        return t.div({ onunmount: () => abort?.abort() }, page("Chat", [crumb("Search"), crumb("Chat with your content")], () => t.div(null,
            t.p({ className: "m-t-0 txt-hint" }, "Retrieval-augmented generation (RAG) over your buckets: each question runs a hybrid search, and the model answers from the passages it finds, citing them. It's ",
                t.code(null, "$llm.text"), " with ", t.code(null, "enrich"), ", as your pages would call it."),
            data.status && !data.status.model ? noModel() : null,
            bucketPicker(data),
            t.div({ className: "flex gap-sm m-b-base", style: "align-items: center; flex-wrap: wrap" },
                t.span({ className: "txt-hint txt-sm" }, "Answered by"),
                () => data.models.length
                    ? t.select({ style: "max-width: 360px", onchange: (e) => { data.model = e.target.value; remember("pocketone.search.askModel", data.model); } },
                        ...data.models.map((m) => t.option({ value: m.id, selected: m.id === data.model }, m.name || m.id)))
                    : t.span({ className: "txt-hint" }, "no text models yet (Settings → LLM)"),
                () => data.turns.length ? t.button({ type: "button", className: "btn xs secondary m-l-auto", onclick: () => (data.turns = []) },
                    t.i({ className: "ri-refresh-line", ariaHidden: true }), t.span({ className: "txt" }, "New conversation")) : null),
            () => t.div(null, ...data.turns.map((x) => t.div({ className: "search-turn" },
                t.div({ className: "search-q" }, x.question),
                x.error ? t.div({ className: "alert danger" }, t.div({ className: "content" }, x.error))
                    : x.answer ? t.div({ className: "search-a", innerHTML: markdown(x.answer) })
                        : t.span({ className: "loader sm" }),
                x.passages.length ? t.details({ className: "search-passages" },
                    t.summary(null, `From ${x.passages.length} passage${x.passages.length === 1 ? "" : "s"}` + (x.cost ? `, ${fmtCost(x.cost)}` : "")),
                    ...x.passages.map((p, i) => t.div({ className: "search-result" },
                        t.div({ className: "flex gap-sm" }, t.strong(null, `[${i + 1}]`),
                            t.a({ className: "label", href: bucketHref(p.bucket) }, p.bucket),
                            p.source && p.source !== p.document ? t.span({ className: "txt-hint" }, p.source + " ›") : null,
                            t.span(null, p.document), p.heading ? t.span({ className: "txt-hint" }, "› " + p.heading) : null),
                        t.pre(null, p.text)))) : null))),
            t.form({ className: "flex gap-sm", onsubmit: ask },
                t.textarea({ rows: 2, style: "flex: 1", placeholder: () => data.turns.length ? "A follow-up" : "Ask anything about what's in your buckets",
                    value: () => data.question, oninput: (e) => (data.question = e.target.value),
                    onkeydown: (e) => { if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); ask(); } } }),
                t.button({ className: () => `btn ${data.asking ? "loading" : ""}`, disabled: () => data.asking }, t.span({ className: "txt" }, "Ask"))))));
    });

    // ---- settings: the app's embedding model

    app.routes.superuserOnly("#/search/settings", () => {
        const data = store({ status: null, models: [], model: "", saving: false });
        let timer = null;
        async function load() {
            try {
                data.status = await send("/status");
            } catch (err) {
                app.checkApiError(err);
            }
            clearTimeout(timer);
            const st = data.status;
            if (st?.model && st.embedded < st.chunks && document.querySelector(".page-search-settings")) timer = setTimeout(load, 2000);
        }
        load();
        embedModels().then((m) => (data.models = m));

        async function save(e) {
            e.preventDefault();
            if (!data.model || data.model === data.status?.model) return;
            const chunks = data.status?.chunks || 0;
            if (chunks && !confirm(`Embed all ${chunks} chunks again with the new model? Search keeps working (by keywords) while it runs.`)) return;
            data.saving = true;
            try {
                data.status = await send("/set-model", { model: data.model });
                data.model = "";
                load();
            } catch (err) {
                app.toasts.error(errText(err));
            }
            data.saving = false;
        }

        function progress(st) {
            const pct = st.chunks ? Math.floor((100 * st.embedded) / st.chunks) : 100;
            return t.div({ className: `alert ${st.embedded < st.chunks ? "info" : "success"} m-b-base` }, t.div({ className: "content" },
                st.embedded < st.chunks
                    ? t.div({ className: "flex gap-sm" }, t.span({ className: "loader sm" }),
                        t.span(null, `Embedding: ${st.embedded} of ${st.chunks} chunks (${pct}%). Search keeps working; chunks not done yet are found by keywords.`))
                    : t.span(null, `All ${st.chunks} chunks are embedded.`),
                t.div({ style: "height: 6px; border-radius: 3px; background: var(--baseAlt2Color); margin-top: 8px; overflow: hidden" },
                    t.div({ style: `height: 100%; width: ${pct}%; background: var(--primaryColor); transition: width 0.5s` }))));
        }

        return t.div({ className: "page-search-settings", onunmount: () => clearTimeout(timer) }, page("Search settings", [crumb("Search"), crumb("Settings")], () => {
            const st = data.status;
            if (!st) return t.div({ className: "block txt-center" }, t.span({ className: "loader lg" }));
            return t.div(null,
                t.div({ className: "txt-lg m-b-sm" }, "Embedding model"),
                t.p({ className: "m-t-0 txt-hint" }, "One model embeds every bucket, so any of them can be searched together. It was picked for you (the cheapest your providers offer); change it here and everything is embedded again in the background."),
                !st.model ? noModel() : t.p(null, "Every bucket is embedded with ", t.code(null, st.model), "."),
                st.model ? progress(st) : null,
                data.models.length ? t.form({ className: "flex gap-sm", onsubmit: save },
                    t.select({ onchange: (e) => (data.model = e.target.value) },
                        t.option({ value: "" }, "Change to…"),
                        ...data.models.map((m) => t.option({ value: m.id, disabled: m.id === st.model }, modelLabel(m)))),
                    t.button({ className: () => `btn ${data.saving ? "loading" : ""}`, disabled: () => !data.model }, t.span({ className: "txt" }, "Use this model"))) : null);
        }));
    });

    // ---- one bucket

    // folderFiles is a dropped folder's files, by their path inside it
    async function folderFiles(entry) {
        const out = [];
        const go = async (dir, prefix) => {
            const reader = dir.createReader();
            let batch;
            do {
                batch = await new Promise((res) => reader.readEntries(res, () => res([])));
                for (const e of batch) {
                    if (e.isDirectory) await go(e, prefix + e.name + "/");
                    else if (e.isFile) out.push({ file: await new Promise((res) => e.file(res, () => res(null))), path: prefix + e.name });
                }
            } while (batch.length);
        };
        await go(entry, "");
        return out.filter((f) => f.file);
    }

    // skipped mirrors what the server leaves out of folders, zips and repositories
    const skipDirs = new Set(["node_modules", "vendor", "__pycache__"]);
    const lockfiles = new Set(["package-lock.json", "yarn.lock", "pnpm-lock.yaml", "bun.lockb", "go.sum", "Cargo.lock", "composer.lock", "Gemfile.lock", "poetry.lock", "uv.lock"]);
    const skipped = (p) => {
        const parts = p.split("/");
        return parts.some((x, i) => x.startsWith(".") || (i < parts.length - 1 && skipDirs.has(x))) || lockfiles.has(parts[parts.length - 1]);
    };

    async function sha256(blob) {
        const buf = await crypto.subtle.digest("SHA-256", await blob.arrayBuffer());
        return [...new Uint8Array(buf)].map((b) => b.toString(16).padStart(2, "0")).join("");
    }

    // uploadRaw sends a file as the request body (a zip, or a file the browser can't read as text)
    async function uploadRaw(bucket, name, file, replace) {
        const q = new URLSearchParams({ bucket, name });
        if (replace) q.set("replace", replace);
        const res = await fetch(app.pb.buildURL(api + "/upload?" + q), {
            method: "POST", body: file, headers: { Authorization: app.pb.authStore.token, "Content-Type": "application/octet-stream" },
        });
        const body = await res.json().catch(() => ({}));
        if (!res.ok) throw new Error(body.message || res.statusText);
        return body;
    }

    const kindIcon = { file: "ri-file-text-line", text: "ri-text", folder: "ri-folder-line", dir: "ri-hard-drive-2-line", zip: "ri-file-zip-line", page: "ri-global-line", git: "ri-git-branch-line", collection: "ri-database-2-line" };
    const fmtWhen = (iso) => (iso ? new Date(iso).toLocaleString() : "");
    // kindName says what a source is, in words
    const kindName = { file: "File", text: "Pasted text", folder: "Dropped folder", dir: "Folder on the server", zip: "Zip",
        page: "Web page", git: "Git repository", collection: "Collection" };
    const everyName = { hour: "every hour", day: "every day", week: "every week" };
    // ago and until: "just now", "4 min ago", "in 3 min", "yesterday"... (the full time is the title)
    function span(ms) {
        const m = Math.round(Math.abs(ms) / 60000);
        if (m < 1) return "";
        if (m < 60) return m + " min";
        const h = Math.round(m / 60);
        if (h < 24) return h + " h";
        const d = Math.round(h / 24);
        return d + (d === 1 ? " day" : " days");
    }
    const ago = (iso) => { const s = span(Date.now() - new Date(iso)); return s ? s + " ago" : "just now"; };
    const until = (iso) => { const s = span(new Date(iso) - Date.now()); return s ? "in " + s : "any moment"; };

    app.routes.superuserOnly("#/search/b/{name}", (route) => {
        const name = decodeURIComponent(route.params.name);
        const data = store({
            loading: true, bucket: null, sources: [], status: null, over: false, adding: "",
            form: "", // "paste" | "url" | "follow" | "settings"
            pasteName: "", pasteText: "", url: "", every: "day", urlKind: "", urlChecking: false, follow: { collection: "", fields: [], filter: "" },
            edit: {}, collections: [], open: {}, docs: {},
        });
        let timer = null;

        async function load() {
            try {
                const [b, l, st] = await Promise.all([send("/bucket?name=" + encodeURIComponent(name)),
                    send("/sources?bucket=" + encodeURIComponent(name)), send("/status")]);
                data.bucket = b;
                data.sources = l.sources;
                data.status = st;
                for (const src of data.sources) if (data.open[src.name]) loadDocs(src.name);
                refreshNav();
            } catch (err) {
                app.checkApiError(err);
            }
            data.loading = false;
            clearTimeout(timer);
            // follow embedding while it runs (not while it waits for a provider: nothing would change)
            const busy = (data.bucket?.pending && data.status?.model) || data.sources.some((x) => x.status === "reading");
            if (busy && document.querySelector(".page-search-bucket")) timer = setTimeout(load, 2000);
        }

        async function loadDocs(source) {
            const d = await send("/documents?bucket=" + encodeURIComponent(name) + "&source=" + encodeURIComponent(source));
            data.docs = { ...data.docs, [source]: d.documents };
        }

        function toggle(source) {
            data.open = { ...data.open, [source]: !data.open[source] };
            if (data.open[source]) loadDocs(source);
        }

        const existing = (n) => data.sources.find((x) => x.name === n);

        // confirmAdd asks before replacing a source of the same name, and before adding content that's already here
        // under another name; it returns false to skip
        async function confirmAdd(n, file) {
            const same = existing(n);
            if (same) return confirm(`Replace ${n} (${same.documents} document${same.documents === 1 ? "" : "s"})? Unchanged files cost nothing; files it no longer has are removed.`);
            if (file) {
                const h = await sha256(file);
                const dup = data.sources.find((x) => x.hash === h);
                if (dup) return confirm(`${n} is the same as ${dup.name}, already in this bucket. Add it again anyway?`);
            }
            return true;
        }

        // addFile adds a loose file or a zip as its own source (replacing the one named replace, when given)
        async function addFile(file, replace) {
            if (!replace && !(await confirmAdd(file.name, file))) return;
            if (/\.zip$/i.test(file.name)) {
                await uploadRaw(name, file.name, file, replace);
            } else {
                if (file.size > MAX_FILE) throw new Error("over 10 MB");
                await send("/add", { bucket: name, name: file.name, text: await file.text(), kind: "file", replace });
            }
        }

        // addFolder sends a dropped folder file by file, then prunes what it no longer has
        async function addFolder(entry, replace) {
            const folder = entry.name;
            if (!replace && existing(folder) && !(await confirmAdd(folder))) return;
            const files = (await folderFiles(entry)).filter((f) => !skipped(f.path) && f.file.size <= 1 << 20);
            const keep = [];
            let failed = 0;
            for (const [i, f] of files.entries()) {
                data.adding = `${folder}: ${i + 1} of ${files.length}, ${f.path}`;
                try {
                    const text = await f.file.text();
                    if (text.includes("\u0000")) continue; // binary
                    await send("/put", { bucket: name, source: folder, path: f.path, text, replace: i === 0 ? replace : undefined });
                    keep.push(f.path);
                } catch (_) {
                    failed++;
                }
            }
            await send("/prune", { bucket: name, source: folder, keep });
            if (failed) app.toasts.error(`${folder}: ${failed} file${failed === 1 ? "" : "s"} not added.`);
        }

        async function onDrop(e) {
            e.preventDefault();
            data.over = false;
            const entries = [...(e.dataTransfer.items || [])].map((i) => i.webkitGetAsEntry?.()).filter(Boolean);
            const files = entries.length ? [] : [...(e.dataTransfer.files || [])];
            try {
                for (const en of entries) {
                    data.adding = "Adding " + en.name;
                    if (en.isDirectory) await addFolder(en);
                    else await addFile(await new Promise((res, rej) => en.file(res, rej)));
                }
                for (const f of files) {
                    data.adding = "Adding " + f.name;
                    await addFile(f);
                }
            } catch (err) {
                app.toasts.error(errText(err));
            }
            data.adding = "";
            load();
        }

        // newVersion picks a file (or zip) that replaces a source, whatever it's called now
        function newVersion(src) {
            const input = document.createElement("input");
            input.type = "file";
            input.onchange = async () => {
                const f = input.files[0];
                if (!f) return;
                data.adding = `Replacing ${src.name} with ${f.name}`;
                try {
                    await addFile(f, src.name);
                } catch (err) {
                    app.toasts.error(errText(err));
                }
                data.adding = "";
                load();
            };
            input.click();
        }

        // searchable are the field types whose text a collection source can search; text and editor fields start checked
        const searchable = new Set(["text", "editor", "email", "url", "select", "json"]);
        function pickCollection(nameOf) {
            const col = data.collections.find((c) => c.name === nameOf);
            data.follow = { ...data.follow, collection: nameOf,
                fields: (col?.fields || []).filter((f) => !f.hidden && !f.system && (f.type === "text" || f.type === "editor")).map((f) => f.name) };
        }

        // detectSoon asks the server how a URL would be read, once typing pauses
        let detectTimer = null;
        function detectSoon() {
            clearTimeout(detectTimer);
            data.urlKind = "";
            const u = data.url.trim();
            if (!/^https?:\/\/[^/]+\.[^/]+/.test(u)) return;
            detectTimer = setTimeout(async () => {
                data.urlChecking = true;
                try {
                    const r = await send("/detect-url", { url: u });
                    if (data.url.trim() === u) data.urlKind = r.kind;
                } catch (_) {}
                data.urlChecking = false;
            }, 600);
        }

        async function submit(path, body, after) {
            try {
                await send(path, body);
                if (data.form) app.modals.close();
                data.form = "";
                after?.();
                load();
                refreshNav();
            } catch (err) {
                app.toasts.error(errText(err));
            }
        }

        function remove(src) {
            app.modals.confirm(`Take ${src.name} out of the bucket, with its ${src.documents} document${src.documents === 1 ? "" : "s"}?`, () =>
                submit("/remove", { bucket: name, sources: [src.name] }));
        }

        async function openForm(form) {
            data.form = data.form === form ? "" : form;
            if (form === "settings") {
                const b = data.bucket;
                data.edit = { title: b.title, description: b.description, maxDocuments: b.maxDocuments, maxMB: Math.round(b.maxBytes / (1 << 20)) };
            }
            if (form === "follow") {
                data.collections = (await app.pb.collections.getFullList({ requestKey: null })).filter((c) => !c.system && !c.name.startsWith("_"));
            }
        }

        function deleteBucket() {
            app.modals.confirm(`Delete the bucket ${name} and everything in it? Apps that search it will find nothing until it's filled again.`, async () => {
                try {
                    await send("/delete", { name });
                    window.location.hash = "#/search";
                } catch (err) {
                    app.toasts.error(errText(err));
                }
            });
        }

        load();

        const crumbs = [crumb("Search"), crumb("Buckets", "#/search"), t.div({ className: "breadcrumb-item" }, () => data.bucket?.title || name,
            () => data.bucket?.description ? t.i({ className: "ri-information-line txt-hint m-l-5", style: "font-size: 0.8em",
                ariaLabel: app.attrs.tooltip((data.bucket.owner ? data.bucket.owner + " wants: " : "") + data.bucket.description) }) : null)];
        const field = (label, input, cls = "col-lg-12") => t.div({ className: cls }, t.div({ className: "field" }, t.label({}, label), input));
        const bound = (obj, key, props = {}) => t.input({ type: "text", ...props, value: () => obj[key], oninput: (e) => (obj[key] = e.target.value) });
        const formButtons = (label) => t.div({ className: "col-lg-12 flex" }, t.div({ className: "m-r-auto" }),
            t.button({ type: "button", className: "btn transparent secondary", onclick: () => (data.form = "") }, t.span({ className: "txt" }, "Cancel")),
            t.button({ className: "btn" }, t.span({ className: "txt" }, label)));

        // docStatus is a document's progress: ready, failed, embedding (with how far), or waiting its turn
        function docStatus(d) {
            if (d.status === "ready") return t.span({ className: "label success" }, "ready");
            if (d.status === "error") return t.span({ className: "label danger", title: d.error }, "error");
            if (!data.status?.model) return t.span({ className: "label warning" }, "waiting for a provider");
            if (d.status === "embedding" || d.embedded > 0) {
                return t.span({ className: "label warning" }, t.span({ className: "loader sm m-r-5" }), `embedding ${d.embedded} of ${d.chunks}`);
            }
            return t.span({ className: "label", title: "Embedded in the order it was added, after what came before it" }, "in line");
        }

        // sourceStatus is how a source is doing, as a label: ready, reading, embedding (how far), waiting, failed
        function sourceStatus(src) {
            if (src.status === "error") return t.span({ className: "label danger" }, "couldn't read it");
            if (src.status === "reading") return t.span({ className: "label warning" }, t.span({ className: "loader sm m-r-5" }), "reading…");
            if (src.pending && !data.status?.model) return t.span({ className: "label warning", title: "Add an AI provider with embedding models in Settings → LLM" }, "waiting for an AI provider");
            if (src.pending) return t.span({ className: "label warning" }, t.span({ className: "loader sm m-r-5" }), `embedding ${src.embedded} of ${src.chunks} chunks`);
            return t.span({ className: "label success" }, "ready to search");
        }

        // sourceAbout is what a source is and where it comes from, in one line
        function sourceAbout(src) {
            const bits = [kindName[src.kind] || src.kind];
            if (src.kind === "page" || src.kind === "git") bits.push(src.every ? "read " + everyName[src.every] : "read when you refresh it");
            if (src.kind === "collection") bits.push(`${src.fields.join(", ")}${src.filter ? " where " + src.filter : ""}`, "kept in sync");
            if (src.kind === "dir") {
                if (src.include?.length) bits.push(src.include.join(", "));
                bits.push("checked every 5 minutes");
                if (src.pageURL) bits.push("links to " + src.pageURL);
            }
            bits.push(`${src.documents} document${src.documents === 1 ? "" : "s"}`, fmtBytes(src.bytes));
            if (src.costMicroUSD) bits.push(fmtCost(src.costMicroUSD) + " to embed");
            return bits.join(" · ");
        }

        // sourceWhen: when it was last read, and when it's read next
        function sourceWhen(src) {
            const parts = [];
            if (src.refreshed) parts.push(t.span({ title: fmtWhen(src.refreshed) }, "read " + ago(src.refreshed)));
            if (src.next) parts.push(t.span({ title: fmtWhen(src.next) }, "next check " + until(src.next)));
            return parts.length ? t.span({ className: "txt-hint" }, ...parts.flatMap((p, i) => (i ? [" · ", p] : [p]))) : null;
        }

        const refreshTitle = {
            page: "Fetch the page again now", git: "Read the repository again now", collection: "Read every record again now",
            zip: "Split the zip again now", dir: "Read the folder again now: changed files are embedded again, removed ones leave",
        };

        // statusIcon is how a source is doing, as one icon (the words are its tooltip)
        function statusIcon(src) {
            const icon = (cls, tip) => t.i({ className: "search-status-icon " + cls, ariaLabel: app.attrs.tooltip(tip) });
            if (src.status === "error") return icon("ri-error-warning-fill txt-danger", "Couldn't read it: " + src.error);
            if (src.status === "reading") return t.span({ className: "loader sm", ariaLabel: app.attrs.tooltip("Reading…") });
            if (src.pending && !data.status?.model) return icon("ri-time-line txt-warning", "Waiting for an AI provider with embedding models (Settings → LLM)");
            if (src.pending) return t.span({ className: "txt-hint txt-sm", ariaLabel: app.attrs.tooltip(`Embedding ${src.embedded} of ${src.chunks} chunks`) },
                t.span({ className: "loader sm m-r-5" }), Math.floor((100 * src.embedded) / Math.max(1, src.chunks)) + "%");
            if (src.error) return icon("ri-error-warning-line txt-warning", src.error);
            return icon("ri-checkbox-circle-fill txt-success", "Ready to search");
        }

        // sourceInfo is the tooltip on a source's info icon: what it is, and when it was read
        function sourceInfo(src) {
            const when = [];
            if (src.refreshed) when.push("read " + ago(src.refreshed));
            if (src.next) when.push("next check " + until(src.next));
            return sourceAbout(src) + (when.length ? "\n" + when.join(" · ") : "");
        }

        const iconBtn = (icon, tip, onclick, cls = "secondary", props = {}) => t.button({ type: "button",
            className: `btn xs circle transparent ${cls}`, ariaLabel: app.attrs.tooltip(tip), onclick, ...props }, t.i({ className: icon, ariaHidden: true }));

        // blank holds an icon's place on a row without it, so every row's icons line up
        const blank = () => t.span({ className: "btn xs circle transparent", style: "visibility: hidden", ariaHidden: true }, t.i({ className: "ri-refresh-line" }));

        function sourceRows(src) {
            const open = data.open[src.name];
            const docs = data.docs[src.name];
            const rows = [t.tr(null,
                t.td({ className: "name" }, t.a({ href: "#", title: src.name, onclick: (e) => { e.preventDefault(); toggle(src.name); } },
                    t.i({ className: (open ? "ri-arrow-down-s-line" : "ri-arrow-right-s-line") + " txt-hint", ariaHidden: true }),
                    t.i({ className: (kindIcon[src.kind] || "ri-file-line") + " txt-hint m-r-5", ariaHidden: true }), src.name)),
                t.td({ className: "search-num txt-hint" }, src.documents + (src.documents === 1 ? " doc" : " docs")),
                t.td({ className: "search-num txt-hint" }, fmtBytes(src.bytes)),
                t.td({ className: "txt-center" }, statusIcon(src)),
                t.td({ className: "icons" },
                    t.span({ className: "btn xs circle transparent secondary", style: "cursor: default", ariaLabel: app.attrs.tooltip(sourceInfo(src)) },
                        t.i({ className: "ri-information-line", ariaHidden: true })),
                    refreshTitle[src.kind] ? iconBtn("ri-refresh-line", refreshTitle[src.kind], () => submit("/refresh", { bucket: name, source: src.name }),
                        "secondary", { disabled: src.status === "reading" }) : blank(),
                    src.kind === "file" || src.kind === "zip" ? iconBtn("ri-upload-2-line", "Upload a new version", () => newVersion(src))
                        : data.sources.some((x) => x.kind === "file" || x.kind === "zip") ? blank() : null,
                    iconBtn("ri-delete-bin-7-line", "Remove from this bucket", () => remove(src), "danger")))];
            if (open) {
                rows.push(t.tr(null, t.td({ colSpan: 5, style: "padding-left: 34px" },
                    !docs ? t.span({ className: "loader sm" }) : !docs.length ? t.span({ className: "txt-hint" }, "No documents.")
                        : t.div({ style: "overflow-x: auto; max-height: 360px; overflow-y: auto" }, t.table({ className: "search-table" }, t.tbody(null,
                            ...docs.slice(0, 200).map((d) => t.tr(null,
                                t.td(null, d.record ? t.a({ href: recordHref(d.record) }, d.name) : d.name, d.error ? t.div({ className: "txt-danger txt-sm" }, d.error) : null),
                                t.td({ className: "search-num txt-hint" }, d.chunks + " chunks"),
                                t.td({ className: "search-num txt-hint" }, fmtBytes(d.size)),
                                t.td(null, docStatus(d)))),
                            docs.length > 200 ? t.tr(null, t.td({ className: "txt-hint" }, `and ${docs.length - 200} more`)) : null))))));
            }
            return rows;
        }

        // ---- modals: add a source, bucket settings

        function addTab(form, icon, label) {
            return t.button({ type: "button", className: () => `btn sm ${data.form === form ? "" : "secondary"}`,
                onclick: () => (data.form === form ? (data.form = "") : openForm(form)) }, t.i({ className: icon, ariaHidden: true }), t.span({ className: "txt" }, label));
        }

        function openAdd() {
            data.form = "";
            showModal(t.div({ className: "modal popup", onafterclose: (el) => { data.form = ""; el.remove(); } },
                t.header({ className: "modal-header" }, t.h5({ className: "m-auto" }, "Add to " + (data.bucket?.title || name))),
                t.div({ className: "modal-content" },
                    t.div({
                        className: () => `search-dropzone m-b-sm ${data.over ? "over" : ""}`,
                        ondragover: (e) => { e.preventDefault(); data.over = true; },
                        ondragleave: () => (data.over = false),
                        ondrop: (e) => { app.modals.close(); onDrop(e); },
                    },
                        () => data.adding ? t.span(null, t.span({ className: "loader sm m-r-5" }), data.adding) : t.span(null,
                            t.i({ className: "ri-upload-cloud-line", ariaHidden: true }), " Drop files, folders or zips here, or ",
                            t.label({ className: "link-primary", style: "cursor: pointer" }, "choose files",
                                t.input({ type: "file", multiple: true, hidden: true, onchange: async (e) => {
                                    const files = [...e.target.files];
                                    app.modals.close();
                                    for (const f of files) {
                                        data.adding = "Adding " + f.name;
                                        try { await addFile(f); } catch (err) { app.toasts.error(`${f.name}: ${errText(err)}`); }
                                    }
                                    data.adding = "";
                                    load();
                                } })), ".")),
                    t.div({ className: "flex gap-sm m-b-sm", style: "flex-wrap: wrap; justify-content: center" },
                        addTab("url", "ri-global-line", "Web page or Git repo"), addTab("paste", "ri-text", "Paste text"), addTab("follow", "ri-database-2-line", "A collection")),
                    () => addForm()),
                t.footer({ className: "modal-footer" },
                    t.button({ type: "button", className: "btn transparent m-l-auto", onclick: () => app.modals.close() }, t.span({ className: "txt" }, "Close")))));
        }

        function openSettings() {
            openForm("settings");
            showModal(t.div({ className: "modal popup", onafterclose: (el) => { data.form = ""; el.remove(); } },
                t.header({ className: "modal-header" }, t.h5({ className: "m-auto" }, "Bucket settings")),
                t.div({ className: "modal-content" }, () => settingsForm())));
        }

        function settingsForm() {
            return t.form({ className: "grid m-b-base", onsubmit: (e) => {
                    e.preventDefault();
                    const x = data.edit;
                    submit("/update", { name, title: x.title, description: x.description, maxDocuments: parseInt(x.maxDocuments) || 0, maxBytes: (parseInt(x.maxMB) || 0) * (1 << 20) });
                } },
                    field("Title", bound(data.edit, "title"), "col-lg-6"),
                    field("What belongs in it", bound(data.edit, "description"), "col-lg-6"),
                    field("Most documents", t.input({ type: "number", min: 0, value: () => data.edit.maxDocuments, oninput: (e) => (data.edit.maxDocuments = e.target.value) }), "col-lg-6"),
                    field("Most text (MB)", t.input({ type: "number", min: 0, value: () => data.edit.maxMB, oninput: (e) => (data.edit.maxMB = e.target.value) }), "col-lg-6"),
                    t.div({ className: "col-lg-12 flex" },
                        t.button({ type: "button", className: "btn sm transparent danger", onclick: deleteBucket },
                            t.i({ className: "ri-delete-bin-7-line", ariaHidden: true }), t.span({ className: "txt" }, "Delete bucket")),
                        t.div({ className: "m-r-auto" }),
                        t.button({ type: "button", className: "btn transparent secondary", onclick: () => app.modals.close() }, t.span({ className: "txt" }, "Cancel")),
                        t.button({ className: "btn" }, t.span({ className: "txt" }, "Save"))));
        }

        // addForm is the add modal's open form: a URL, pasted text or a collection ("" shows none)
        function addForm() {
            if (data.form === "paste") return t.form({ className: "grid m-b-base", onsubmit: async (e) => {
                    e.preventDefault();
                    const n = data.pasteName.trim();
                    if (!(await confirmAdd(n))) return;
                    submit("/add", { bucket: name, name: n, text: data.pasteText, kind: "text" }, () => (data.pasteName = data.pasteText = ""));
                } },
                    field("Name", bound(data, "pasteName", { required: true, placeholder: "characters.md" })),
                    field("Text", t.textarea({ rows: 8, required: true, value: () => data.pasteText, oninput: (e) => (data.pasteText = e.target.value) })),
                    formButtons("Add"));
            if (data.form === "url") return t.form({ className: "grid m-b-base", onsubmit: async (e) => {
                    e.preventDefault();
                    const u = data.url.trim();
                    if (!(await confirmAdd(u))) return;
                    submit("/add-url", { bucket: name, url: u, every: data.every, kind: data.urlKind || undefined }, () => (data.url = data.urlKind = ""));
                } },
                    field("A web page or a Git repository", t.input({ type: "url", required: true,
                        placeholder: "https://pocketone.dev/llms.txt or https://github.com/owner/repo",
                        value: () => data.url, oninput: (e) => { data.url = e.target.value; detectSoon(); } }), "col-lg-8"),
                    field("Read it again", t.select({ onchange: (e) => (data.every = e.target.value) },
                        ...[["", "Only when I refresh it"], ["hour", "Every hour"], ["day", "Every day"], ["week", "Every week"]].map(([v, l]) =>
                            t.option({ value: v, selected: v === data.every }, l))), "col-lg-4"),
                    t.div({ className: "col-lg-12" }, () => {
                        if (data.urlChecking) return t.span({ className: "txt-hint" }, t.span({ className: "loader sm m-r-5" }), "Asking the server whether it's a Git repository…");
                        if (!data.urlKind) return t.span({ className: "txt-hint txt-sm" }, "The server is asked whether it's a Git repository; anything else is read as a web page.");
                        const pick = (kind, label) => t.button({ type: "button", className: () => `btn xs ${data.urlKind === kind ? "" : "secondary"}`,
                            onclick: () => (data.urlKind = kind) }, label);
                        return t.div({ className: "flex gap-sm", style: "align-items: center; flex-wrap: wrap" },
                            t.span(null, "Will be read as"), pick("git", "Git repository"), pick("page", "Web page"),
                            t.span({ className: "txt-hint txt-sm" }, data.urlKind === "git"
                                ? "Its default branch's text files and recent commits (no binaries, lockfiles, node_modules or vendor)."
                                : "One document: the page's text (HTML as Markdown)."));
                    }),
                    formButtons("Add URL"));
            if (data.form === "follow") return t.form({ className: "grid m-b-base", onsubmit: (e) => {
                    e.preventDefault();
                    const f = data.follow;
                    if (!f.fields.length) return app.toasts.error("Check at least one field to search.");
                    submit("/follow", { bucket: name, collection: f.collection, fields: f.fields, filter: f.filter.trim() });
                } },
                    field("Collection", t.select({ required: true, onchange: (e) => pickCollection(e.target.value) },
                        t.option({ value: "" }, "Pick one"),
                        ...data.collections.map((c) => t.option({ value: c.name, selected: c.name === data.follow.collection }, c.name))), "col-lg-6"),
                    field("Only records matching (optional)", bound(data.follow, "filter", { placeholder: "published = true" }), "col-lg-6"),
                    () => {
                        const col = data.collections.find((c) => c.name === data.follow.collection);
                        if (!col) return null;
                        const fields = col.fields.filter((f) => !f.hidden && !f.primaryKey && searchable.has(f.type));
                        const on = (n) => data.follow.fields.includes(n);
                        const flip = (n) => (data.follow.fields = on(n) ? data.follow.fields.filter((x) => x !== n)
                            : fields.map((f) => f.name).filter((x) => x === n || on(x))); // keep the collection's order
                        return t.div({ className: "col-lg-12" },
                            t.div({ className: "m-b-5" }, "Fields to search ",
                                t.span({ className: "txt-hint txt-sm" }, data.follow.fields.length
                                    ? `(${data.follow.fields[0]} is each record's title)` : "(check one or more)")),
                            fields.length
                                ? t.div({ className: "flex gap-sm", style: "flex-wrap: wrap" }, ...fields.map((f) => t.label({
                                    className: () => `btn xs ${on(f.name) ? "" : "secondary"}`, style: "cursor: pointer",
                                }, t.input({ type: "checkbox", checked: () => on(f.name), onchange: () => flip(f.name), style: "margin-right: 6px" }),
                                    f.name, t.span({ className: "txt-hint txt-sm m-l-5" }, f.type))))
                                : t.span({ className: "txt-hint" }, "This collection has no text fields to search."));
                    },
                    formButtons("Follow"));
            return null;
        }

        return t.div({ className: "page-search-bucket", onunmount: () => clearTimeout(timer) }, page(name, crumbs, () => {
            if (data.loading) return t.div({ className: "block txt-center" }, t.span({ className: "loader lg" }));
            const b = data.bucket;
            if (!b) return t.div({ className: "block txt-center txt-hint p-base" }, "No bucket named ", t.code(null, name), ".");
            return t.div(null,
                data.status && !data.status.model ? noModel() : null,
                () => data.adding ? t.div({ className: "txt-hint m-b-sm" }, t.span({ className: "loader sm m-r-5" }), data.adding) : null,
                data.sources.length
                    ? t.table({ className: "search-table search-rows" }, t.tbody(null, ...data.sources.flatMap(sourceRows)))
                    : t.div({ className: "block txt-center txt-hint p-base" }, "Nothing in this bucket yet. ",
                        t.a({ href: "#", onclick: (e) => { e.preventDefault(); openAdd(); } }, "Add something"), "."),
                data.sources.length ? t.div({ className: "search-totals" },
                    `${b.sources} source${b.sources === 1 ? "" : "s"} · ${b.documents} documents · ${b.chunks} chunks · ${fmtBytes(b.bytes)}`) : null,
            );
        }, { secondary: [
            t.a({ className: "btn circle transparent secondary", href: "#/search/find?buckets=" + encodeURIComponent(name), ariaLabel: app.attrs.tooltip("Search this bucket") },
                t.i({ className: "ri-search-line", ariaHidden: true })),
            t.a({ className: "btn circle transparent secondary", href: "#/search/chat?buckets=" + encodeURIComponent(name), ariaLabel: app.attrs.tooltip("Chat with this bucket") },
                t.i({ className: "ri-sparkling-2-line", ariaHidden: true })),
            t.button({ type: "button", className: "btn circle transparent secondary", ariaLabel: app.attrs.tooltip("Bucket settings"), onclick: openSettings },
                t.i({ className: "ri-settings-3-line", ariaHidden: true }))],
        primary: [
            t.button({ type: "button", className: "btn", onclick: openAdd },
                t.i({ className: "ri-add-line", ariaHidden: true }), t.span({ className: "txt" }, "Add source"))] }));
    });
})();
