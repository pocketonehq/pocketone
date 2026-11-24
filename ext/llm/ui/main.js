// Settings → LLM for the PocketBase dashboard (PocketOne ext/llm): the providers $llm calls name as provider/model,
// each with its endpoint and key, and the limits every call passes first. Loaded as a UI extension (/_/extensions/llm/main.js). A view over llm's settings/v1
// section, built from the stock settings pages' pieces (accordions, fields with help under them, the save footer).
(() => {
    // window.pocketone.llm: models and their prices for any dashboard screen (this one's model browser, an app's own
    // settings screen). Prices come in the llm contracts' units; each is shown in its kind's own unit (text per million
    // tokens, audio per minute, pictures per picture or megapixel) with where it comes from: listed by the provider,
    // measured from this app's billed calls, or unknown.
    const llm = {};
    const cache = {}; // kind → promise of its models, for this page load
    llm.KINDS = ["text", "image", "speech", "transcribe", "align", "music", "video", "embed"];
    llm.KIND_LABELS = { text: "Text", image: "Pictures", speech: "Speech", transcribe: "Transcription", align: "Word timing",
        music: "Music", video: "Video", embed: "Embeddings" };

    // models is every model of kind across the providers in Settings → LLM (cached for the page; fresh: ask again).
    llm.models = (kind, fresh) => {
        if (fresh || !cache[kind]) {
            cache[kind] = app.pb.send(`/api/_c/llm.${kind}/v1/models`, { requestKey: null })
                .then((r) => r.models || [])
                .catch((err) => { delete cache[kind]; throw err; });
        }
        return cache[kind];
    };

    // usd formats dollars with as many digits as a small price needs.
    llm.usd = (n) => {
        if (!n) return "$0";
        if (n >= 1) return "$" + n.toFixed(2);
        if (n >= 0.01) return "$" + n.toFixed(3);
        return "$" + Number(n.toPrecision(2)).toString();
    };

    const listed = (m, unit) => (m.prices || []).find((p) => p.unit === unit && p.source !== "measured");
    const measured = (m) => (m.prices || []).find((p) => p.source === "measured");
    const per = { second: [60, "min"], megapixel: [1, "megapixel"], image: [1, "picture"], "1K characters": [1, "1K characters"],
        request: [1, "call"], "1M tokens": [1, "1M tokens"], "1M input tokens": [1, "1M input tokens"],
        "1M output tokens": [1, "1M output tokens"], "1M cached input tokens": [1, "1M cached input tokens"] };
    const one = (p) => { const [k, u] = per[p.unit] || [1, p.unit]; return `${llm.usd(p.usd * k)}/${u}`; };

    // prices is a model's prices as short lines: { text, source, calls }. Text models put input and output on one line.
    llm.prices = (m) => {
        const out = [];
        const inP = listed(m, "1M input tokens"), outP = listed(m, "1M output tokens");
        if (inP || outP) {
            out.push({ source: "listed", text: [inP ? `${llm.usd(inP.usd)} in` : null, outP ? `${llm.usd(outP.usd)} out` : null]
                .filter(Boolean).join(" · ") + " per 1M tokens" });
        }
        for (const p of m.prices || []) {
            if (p.source === "measured" || ["1M input tokens", "1M output tokens", "1M cached input tokens"].includes(p.unit)) continue;
            out.push({ source: "listed", text: one(p) });
        }
        const mp = measured(m);
        if (mp) out.push({ source: "measured", calls: mp.calls, text: `${one(mp)} measured over ${mp.calls} call${mp.calls === 1 ? "" : "s"}` });
        return out;
    };

    // priceText is a model's prices on one line, or "price unknown".
    llm.priceText = (m) => llm.prices(m).map((p) => p.text).join("; ") || "price unknown";

    // rank is a number to sort a kind's models by price (cheapest first; unknown is Infinity): what its main meter costs,
    // listed if the provider lists it, else measured. Tokens are mixed three in to one out, as a typical call reads more
    // than it writes.
    llm.rank = (m) => {
        const mp = measured(m);
        const pick = (...units) => { for (const u of units) { const p = listed(m, u); if (p) return p.usd; } return null; };
        const inP = listed(m, "1M input tokens"), outP = listed(m, "1M output tokens");
        if (inP && outP) return (3 * inP.usd + outP.usd) / 4;
        let v = pick("1M output tokens", "1M input tokens", "second", "image", "megapixel", "1K characters", "request");
        if (v == null && mp) v = mp.usd;
        return v == null ? Infinity : v;
    };

    // costFor is what a model charges for a job, in dollars: job is how much of each meter the work uses, in the llm
    // contracts' price units ({ "1M input tokens": 0.006, "1M output tokens": 0.002 } is 6,000 tokens in and 2,000 out;
    // { second: 60 } is a minute of audio). Listed prices first; a model with none for the job's meters is priced at its
    // measured rate when that meter is in the job. null: the model's prices don't cover this job (or it lists none).
    llm.costFor = (m, job) => {
        if (!job) return null;
        const prices = m.prices || [];
        const pick = (source) => {
            let usd = 0, any = false;
            for (const p of prices) {
                if ((source === "measured") !== (p.source === "measured")) continue;
                const n = job[p.unit];
                if (n == null) continue;
                usd += p.usd * n;
                any = true;
            }
            return any ? usd : null;
        };
        return pick("listed") ?? pick("measured");
    };

    // partialFor is true when a model's listed prices cover only part of a text job: it lists an input price or an
    // output price but not both, so costFor leaves the other side out and the cost is at least what it says.
    llm.partialFor = (m, job) => {
        if (!job || job["1M input tokens"] == null || job["1M output tokens"] == null) return false;
        const has = (u) => (m.prices || []).some((p) => p.unit === u && p.source !== "measured");
        return has("1M input tokens") !== has("1M output tokens") && !has("1M tokens");
    };

    // label is what a model is called: its name, else its id.
    llm.label = (m) => m.name || m.id.slice(m.id.indexOf("/") + 1);

    // select is a searchable dropdown of kind's models, each with its prices and a line of its description; value() is
    // the chosen provider/model and onchange(id) gets a new one. A value no provider lists (a provider not reachable
    // now) stays as it is. job() (optional) is the work the model is picked for, as costFor takes it, and per names it
    // ("a story"): each model then also shows what that work costs on it ("≈ $0.0012 a story"), and the list is sorted
    // by that, cheapest first; a model that lists only one side of a text job's tokens shows "≥" and comes after the
    // fully priced ones, and models whose prices don't cover the job come last.
    llm.select = ({ kind, value, onchange, required = true, job = null, per = "a call" }) => {
        const state = store({ models: null, error: "", value: value() });
        llm.models(kind).then((ms) => { state.models = ms.slice().sort((a, b) => llm.rank(a) - llm.rank(b) || a.id.localeCompare(b.id)); })
            .catch((err) => { state.error = err?.response?.message || err?.message || String(err); state.models = []; });
        return t.div({ className: "llm-model-select" }, () => {
            const current = state.value;
            const work = job ? job() : null;
            const cost = new Map((state.models || []).map((m) => [m.id, llm.costFor(m, work)]));
            const partial = new Map((state.models || []).map((m) => [m.id, llm.partialFor(m, work)]));
            // fully priced models first, cheapest first; then those priced on one side only; then unknown
            const key = (m) => (cost.get(m.id) == null ? 2 : partial.get(m.id) ? 1 : 0);
            const ms = work ? (state.models || []).slice().sort((a, b) => key(a) - key(b)
                || (cost.get(a.id) ?? Infinity) - (cost.get(b.id) ?? Infinity) || llm.rank(a) - llm.rank(b) || a.id.localeCompare(b.id))
                : state.models || [];
            const options = ms.map((m) => ({
                value: m.id,
                selected: m.id,
                label: () => t.div({ className: "llm-model-option" },
                    t.div(null, t.strong(null, llm.label(m)), " ", t.span({ className: "txt-hint" }, m.id)),
                    t.div({ className: "txt-sm" }, work ? t.strong(null, cost.get(m.id) == null ? `cost ${per} unknown · `
                        : `${partial.get(m.id) ? "≥" : "≈"} ${llm.usd(cost.get(m.id))} ${per} · `) : null, llm.priceText(m), m.size ? ` · ${m.size}` : ""),
                    m.description ? t.div({ className: "txt-sm txt-hint llm-model-desc" }, m.description) : null),
            }));
            if (current && !ms.some((m) => m.id === current)) options.unshift({ value: current, label: current });
            return app.components.select({
                required, searchThreshold: 0, placeholder: state.models ? "- Select a model -" : "Loading models…",
                options: () => options, value: () => state.value,
                onchange: (sel) => { if (sel?.[0] && sel[0].value !== state.value) { state.value = sel[0].value; onchange(sel[0].value); } },
            });
        }, () => (state.error ? t.div({ className: "field-help txt-danger" }, state.error) : null));
    };

    // browser is every model across the providers, by kind: search, sort by price or name, with each one's size,
    // features, prices and description.
    llm.browser = ({ kinds = llm.KINDS, kind = kinds[0] } = {}) => {
        const state = store({ kind, models: null, error: "", search: "", sort: "price", limit: 100 });
        const load = (fresh) => {
            state.models = null;
            state.error = "";
            llm.models(state.kind, fresh).then((ms) => { state.models = ms; })
                .catch((err) => { state.error = err?.response?.message || err?.message || String(err); state.models = []; });
        };
        load();
        const rows = () => {
            const q = state.search.trim().toLowerCase();
            let ms = (state.models || []).filter((m) => !q || [m.id, m.name, m.description, m.size, ...(m.features || [])]
                .some((x) => x && String(x).toLowerCase().includes(q)));
            ms = ms.slice().sort(state.sort === "price" ? (a, b) => llm.rank(a) - llm.rank(b) || a.id.localeCompare(b.id)
                : (a, b) => llm.label(a).localeCompare(llm.label(b)));
            return ms;
        };
        return t.div({ className: "llm-model-browser" },
            t.div({ className: "grid m-b-sm" },
                t.div({ className: "col-md-4" }, t.div({ className: "field" }, t.label(null, "Kind"),
                    app.components.select({ required: true, options: () => kinds.map((k) => ({ value: k, label: llm.KIND_LABELS[k] || k })),
                        value: () => state.kind, onchange: (sel) => { if (sel?.[0] && sel[0].value !== state.kind) { state.kind = sel[0].value; state.limit = 100; load(); } } }))),
                t.div({ className: "col-md-5" }, t.div({ className: "field" }, t.label(null, "Search"),
                    t.input({ type: "search", placeholder: "name, feature, size…", value: () => state.search,
                        oninput: (e) => { state.search = e.target.value; state.limit = 100; } }))),
                t.div({ className: "col-md-3" }, t.div({ className: "field" }, t.label(null, "Sort"),
                    app.components.select({ required: true, options: () => [{ value: "price", label: "Cheapest first" }, { value: "name", label: "By name" }],
                        value: () => state.sort, onchange: (sel) => sel?.[0] && (state.sort = sel[0].value) }))),
            ),
            () => {
                if (state.error) return t.div({ className: "alert danger" }, t.div({ className: "content" }, state.error));
                if (!state.models) return t.div({ className: "block txt-center" }, t.span({ className: "loader" }));
                const ms = rows();
                if (!ms.length) return t.div({ className: "block txt-center txt-hint p-base" }, "No models.");
                return t.div(null,
                    t.p({ className: "txt-hint txt-sm m-t-0" }, `${ms.length} model${ms.length === 1 ? "" : "s"}. `,
                        "Listed prices are the provider's own; measured ones are what this app's calls were billed. A price can vary with options (quality, size)."),
                    t.table({ className: "table llm-models" },
                        t.thead(null, t.tr(null, t.th(null, "Model"), t.th(null, "Price"), t.th(null, "About"))),
                        t.tbody(null, ...ms.slice(0, state.limit).map((m) => t.tr(null,
                            t.td({ style: "min-width: 14em; max-width: 22em" }, t.div(null, t.strong(null, llm.label(m))), t.div({ className: "txt-hint txt-sm" }, t.code(null, m.id)),
                                m.size ? t.div({ className: "txt-sm" }, m.size) : null),
                            t.td({ style: "min-width: 15em" }, ...(llm.prices(m).length
                                ? llm.prices(m).map((p) => t.div({ className: "txt-sm" }, p.text, " ",
                                    t.span({ className: `label sm ${p.source === "measured" ? "success" : ""}` }, p.source)))
                                : [t.span({ className: "txt-hint txt-sm" }, "unknown")])),
                            t.td(null,
                                (m.features?.length || m.reasoning || m.languages?.length || m.contextTokens)
                                    ? t.div({ className: "flex gap-5 m-b-5", style: "flex-wrap: wrap" },
                                        m.reasoning ? t.span({ className: "label sm" }, "reasoning") : null,
                                        ...(m.features || []).map((f) => t.span({ className: "label sm" }, f)),
                                        m.contextTokens ? t.span({ className: "label sm" }, `${Math.round(m.contextTokens / 1024)}K context`) : null,
                                        m.languages?.length ? t.span({ className: "label sm", title: m.languages.join(", ") }, `${m.languages.length} languages`) : null)
                                    : null,
                                m.description ? t.div({ className: "txt-sm" }, m.description) : null),
                        ))),
                    ),
                    ms.length > state.limit
                        ? t.div({ className: "txt-center m-t-sm" }, t.button({ type: "button", className: "btn sm secondary",
                            onclick: () => (state.limit += 100) }, t.span({ className: "txt" }, `Show more (${ms.length - state.limit} left)`)))
                        : null,
                );
            },
        );
    };

    window.pocketone = window.pocketone || {};
    window.pocketone.llm = llm;
})();

(() => {
    const SYSTEM = "System";
    app.store.settingsNavGroups[SYSTEM] = [
        ...app.store.settingsNavGroups[SYSTEM],
        { href: "#/settings/llm", icon: "ri-sparkling-2-line", label: "LLM" },
    ];

    const settingsAPI = (path, body) =>
        app.pb.send("/api/_c/settings/v1" + path, body ? { method: "POST", body, requestKey: null } : { requestKey: null });

    function sidebar() {
        return app.components.pageSidebar(
            { className: "settings-sidebar" },
            t.nav({ className: "sidebar-content scrollable" }, () => {
                const out = [];
                for (const group in app.store.settingsNavGroups) {
                    out.push(t.details(
                        { className: "nav-group", open: true },
                        t.summary({ tabIndex: -1, onfocusout: () => false, onclick: () => false, onkeyup: () => false }, group),
                        ...app.store.settingsNavGroups[group].map((link) => t.a(
                            { href: link.href, className: () => `nav-item ${app.utils.isActivePath(link.href, false) ? "active" : ""}` },
                            link.icon ? t.i({ className: link.icon, ariaHidden: true }) : null,
                            t.span({ className: "txt" }, link.label),
                        )),
                    ));
                }
                return out;
            }),
        );
    }

    const icons = { vultr: "ri-server-line", cloudflare: "ri-cloud-line", openrouter: "ri-route-line", openai: "ri-plug-line" };
    const errText = (err) => err?.response?.message || err?.message || String(err);

    // field is a stock form field: the box (label and input), with its help text under it.
    function field(label, input, hint) {
        return t.div(null, t.div({ className: "field" }, t.label({}, label), input), hint ? t.div({ className: "field-help" }, hint) : null);
    }

    app.routes.superuserOnly("#/settings/llm", () => {
        app.store.title = "LLM";
        const data = store({
            loading: true,
            saving: false,
            view: null, // the section's settings/v1 values
            providers: [], // editable: { name, type, endpoint, account, key, keySet }
            limits: [], // editable: { scope, window, metric, amount, kind, model }
            saved: "", // values() as last loaded or saved, to tell when there are changes
            message: null, // { kind: "success" | "danger", text }
        });

        const typeOf = (type) => (data.view?.types || []).find((x) => x.type === type) || { type, title: type };

        function values() {
            return {
                providers: data.providers.map((p) => ({
                    name: p.name.trim(), type: p.type, endpoint: p.endpoint.trim(), account: p.account.trim(), key: p.key,
                })),
                limits: data.limits.map((l) => ({
                    scope: l.scope, window: l.window, metric: l.metric, amount: Number(l.amount) || 0,
                    kind: l.kind || "", model: (l.model || "").trim(),
                })),
            };
        }
        const hasChanges = () => JSON.stringify(values()) !== data.saved;

        // open holds the uids of the expanded providers: kept outside the store, so editing a field (which redraws the
        // list) doesn't fold them up
        const open = new Set();
        let nextUID = 1;

        function fill(v) {
            data.view = v;
            data.providers = v.providers.map((p) => ({
                uid: nextUID++, name: p.name, type: p.type, endpoint: p.endpoint || "", account: p.account || "", key: "",
                keySet: p.keySet, keyHint: p.keyHint || "",
            }));
            data.limits = v.limits.map((l) => ({ uid: nextUID++, ...l, kind: l.kind || "", model: l.model || "" }));
            data.saved = JSON.stringify(values());
        }

        async function load() {
            try {
                fill((await settingsAPI("/get?extension=llm")).values);
            } catch (err) {
                app.checkApiError(err);
            }
            data.loading = false;
        }

        async function save(e) {
            e?.preventDefault();
            data.saving = true;
            data.message = null;
            try {
                fill((await settingsAPI("/save", { extension: "llm", values: values() })).values);
                app.toasts.success("Saved.");
            } catch (err) {
                data.message = { kind: "danger", text: errText(err) };
            }
            data.saving = false;
        }

        // test checks one provider's key as the card shows it (a key typed but not saved yet included), and shows the
        // answer in the card.
        async function test(i) {
            const uid = data.providers[i].uid;
            const at = () => data.providers.findIndex((p) => p.uid === uid);
            set(i, { testing: true, result: null });
            let result;
            try {
                const r = await settingsAPI("/test", { extension: "llm", values: { providers: [values().providers[i]] } });
                result = r.ok ? { kind: "success", text: "The provider answered with this key." } : { kind: "danger", text: r.message };
            } catch (err) {
                result = { kind: "danger", text: errText(err) };
            }
            if (at() >= 0) set(at(), { testing: false, result });
        }

        // set replaces one provider (so the page redraws), merging changes.
        const set = (i, changes) => (data.providers = data.providers.map((p, j) => (j === i ? { ...p, result: null, ...changes } : p)));

        // freeName is the type's suggested name, numbered when another provider has it.
        function freeName(type, except) {
            const base = typeOf(type).name || type;
            let name = base;
            for (let n = 2; data.providers.some((p, j) => j !== except && p.name === name); n++) name = base + n;
            return name;
        }

        function add() {
            const type = data.view?.types?.[0]?.type || "openai";
            const p = { uid: nextUID++, name: freeName(type), type, endpoint: "", account: "", key: "", keySet: false, keyHint: "" };
            open.add(p.uid);
            data.providers = [...data.providers, p];
            data.message = null;
        }

        // setType switches a provider's type: its endpoint and account go, and a name still at the old type's suggestion
        // follows the new one. A saved key is kept only while the type, endpoint and account stay the same (the server
        // decides), so a new type needs its key entered.
        function setType(i, type) {
            const p = data.providers[i];
            if (!type || type === p.type) return;
            const suggested = (t) => p.name === (typeOf(t).name || t) || new RegExp("^" + (typeOf(t).name || t) + "\\d+$").test(p.name);
            set(i, { type, endpoint: "", account: "", name: !p.name || suggested(p.type) ? freeName(type, i) : p.name });
        }

        function remove(i) {
            data.providers = data.providers.filter((_, j) => j !== i);
        }

        // Limits: scope × window × metric ≤ amount, optionally one kind or one model.
        const SCOPES = [
            { value: "app", label: "The whole app" },
            { value: "user", label: "Each signed-in user" },
            { value: "guest", label: "Each guest" },
            { value: "guests", label: "All guests together" },
        ];
        const METRICS = [
            { value: "cost", label: "US dollars" },
            { value: "output", label: "Output tokens" },
            { value: "input", label: "Input tokens" },
            { value: "calls", label: "Calls" },
        ];
        const WINDOWS = [{ value: "day", label: "a day (UTC)" }, { value: "month", label: "a month (UTC)" }];
        const setLimit = (i, changes) => (data.limits = data.limits.map((l, j) => (j === i ? { ...l, ...changes } : l)));
        function addLimit() {
            // the first one an app needs is usually the guest total: guest calls are refused until it's there
            const scope = data.limits.some((l) => l.scope === "guests") ? "app" : "guests";
            data.limits = [...data.limits, { uid: nextUID++, scope, window: "day", metric: "cost", amount: 1, kind: "", model: "" }];
            data.message = null;
        }
        const money = (micro) => "$" + (micro / 1e6).toFixed(micro && micro < 10000 ? 4 : 2);

        function limitRow(l, i) {
            const pick = (options, value, onpick) => app.components.select({
                required: true, options: () => options, value: () => value, onchange: (sel) => sel?.[0] && onpick(sel[0].value),
            });
            return t.div(
                { className: "grid m-b-sm" },
                i > 0 ? t.div({ className: "col-lg-12" }, t.hr({ className: "m-0" })) : null,
                t.div({ className: "col-md-4" }, field("Who", pick(SCOPES, l.scope, (v) => setLimit(i, { scope: v })))),
                t.div({ className: "col-md-3" }, field("Counts", pick(METRICS, l.metric, (v) => setLimit(i, { metric: v })))),
                t.div({ className: "col-md-2" }, field(l.metric === "cost" ? "At most ($)" : "At most",
                    t.input({ type: "number", min: 0, step: l.metric === "cost" ? "0.01" : "1", required: true, value: l.amount,
                        onchange: (e) => setLimit(i, { amount: e.target.value }) }))),
                t.div({ className: "col-md-3" }, field("Per", pick(WINDOWS, l.window, (v) => setLimit(i, { window: v })))),
                t.div({ className: "col-md-4" }, field("Kind",
                    pick([{ value: "", label: "Every kind" }, ...(data.view?.kinds || []).map((k) => ({ value: k, label: k }))],
                        l.kind, (v) => setLimit(i, { kind: v })))),
                t.div({ className: "col-md-6" }, field("Model",
                    t.input({ type: "text", value: l.model, placeholder: "every model (or provider/model)", onchange: (e) => setLimit(i, { model: e.target.value }) }))),
                t.div({ className: "col-md-2 flex" },
                    t.button({ type: "button", className: "btn sm transparent danger m-l-auto", onclick: () => (data.limits = data.limits.filter((_, j) => j !== i)),
                        ariaDescription: app.attrs.tooltip("Remove limit", "left") },
                        t.i({ className: "ri-delete-bin-7-line", ariaHidden: true }))),
            );
        }

        function providerAccordion(p, i) {
            const ti = typeOf(p.type);
            return t.details(
                { className: "accordion", open: open.has(p.uid), ontoggle: (e) => (e.target.open ? open.add(p.uid) : open.delete(p.uid)) },
                t.summary(
                    null,
                    t.i({ className: icons[p.type] || "ri-plug-line", ariaHidden: true }),
                    t.span({ className: "txt" }, ti.title),
                    t.span({ className: "label" }, `${p.name || "?"}/…`),
                    t.div({ className: "flex-fill" }),
                    // a key is set (saved or just typed): green once it passed its test, gray until then
                    p.key || p.keySet
                        ? p.result?.kind === "success"
                            ? t.i({ className: "ri-checkbox-circle-fill txt-success", ariaDescription: app.attrs.tooltip("Tested: the provider answered with this key", "left") })
                            : t.i({ className: "ri-checkbox-circle-line txt-hint", ariaDescription: app.attrs.tooltip("Key set, not tested yet", "left") })
                        : null,
                ),
                t.p({ className: "m-t-0 txt-hint" }, ti.about || ""),
                t.div(
                    { className: "grid" },
                    t.div({ className: "col-md-6" }, field("Type",
                        app.components.select({
                            required: true,
                            options: () => (data.view?.types || []).map((x) => ({ value: x.type, label: x.title })),
                            value: () => p.type,
                            onchange: (sel) => setType(i, sel?.[0]?.value),
                        }))),
                    t.div({ className: "col-md-6" }, field("Name",
                        t.input({ type: "text", required: true, value: p.name, onchange: (e) => set(i, { name: e.target.value }) }),
                        t.span(null, "Models are named ", t.code(null, `${p.name || "name"}/…`)))),
                    ti.account
                        ? t.div({ className: "col-lg-12" }, field("Account ID",
                            t.input({ type: "text", value: p.account, autocomplete: "off", onchange: (e) => set(i, { account: e.target.value }) }),
                            "In your Cloudflare dashboard's URL, or under Account home."))
                        : t.div({ className: "col-lg-12" }, field("Endpoint",
                            t.input({ type: "url", value: p.endpoint, placeholder: ti.endpoint || "https://…/v1", onchange: (e) => set(i, { endpoint: e.target.value }) }),
                            ti.endpoint ? "Leave empty for the default." : "Include the version path, like /v1.")),
                    t.div({ className: "col-lg-12" }, field(ti.account ? "API token" : "API key",
                        t.input({ type: "password", autocomplete: "new-password", value: p.key,
                            placeholder: p.keySet ? `${p.keyHint || "Saved"} (leave empty to keep it)` : "", onchange: (e) => set(i, { key: e.target.value }) }),
                        ti.account ? "A token with Workers AI permission." : null)),
                    p.result
                        ? t.div({ className: "col-lg-12" }, t.div({ className: `alert ${p.result.kind} m-b-0` }, t.div({ className: "content" }, p.result.text)))
                        : null,
                    t.div({ className: "col-lg-12 flex" },
                        t.button({
                            type: "button",
                            className: `btn sm secondary ${p.testing ? "loading" : ""}`,
                            disabled: !!p.testing,
                            onclick: () => test(i),
                        }, t.i({ className: "ri-checkbox-circle-line", ariaHidden: true }), t.span({ className: "txt" }, "Test")),
                        t.div({ className: "m-r-auto" }),
                        t.button({ type: "button", className: "btn sm transparent danger", onclick: () => remove(i) },
                            t.i({ className: "ri-delete-bin-7-line", ariaHidden: true }), t.span({ className: "txt" }, "Remove provider"))),
                ),
            );
        }

        // modelsAccordion is every model the saved providers offer, by kind, with prices: built the first time it's opened
        // (listing asks each provider), and asked again after a save.
        function modelsAccordion() {
            const body = t.div();
            let built = false;
            return t.details({ className: "accordion", ontoggle: (e) => {
                if (e.target.open && !built) {
                    built = true;
                    body.replaceChildren(window.pocketone.llm.browser());
                }
            } },
            t.summary(null, t.i({ className: "ri-list-check-2", ariaHidden: true }), t.span({ className: "txt" }, "Models"),
                t.span({ className: "label" }, "what your providers offer, with prices")),
            t.p({ className: "m-t-0 txt-hint" }, "Every model of each kind across the saved providers, as ", t.code(null, "provider/model"),
                ". Prices are in each kind's own unit: text per million tokens, audio per minute, pictures per picture or megapixel."),
            body);
        }

        load();

        return t.div(
            { className: "page page-llm-settings" },
            sidebar(),
            t.div(
                { className: "page-content full-height" },
                t.header({ className: "page-header" },
                    t.nav({ className: "breadcrumbs" },
                        t.div({ className: "breadcrumb-item" }, "Settings"),
                        t.div({ className: "breadcrumb-item" }, "LLM"))),
                t.div(
                    { className: "wrapper m-b-base" },
                    () => {
                        if (data.loading) {
                            return t.div({ className: "block txt-center" }, t.span({ className: "loader lg" }));
                        }
                        return t.form(
                            { className: "grid", onsubmit: save },
                            t.div({ className: "col-lg-12" },
                                t.p({ className: "m-t-0" },
                                    "The cloud providers behind ", t.code(null, "$llm"), " in your pages, each on your own key. ",
                                    "Every call names its model as ", t.code(null, "provider/model"), ": with a provider named ",
                                    t.code(null, "vultr"), ", ", t.code(null, "vultr/glm-5.3"), " is its glm-5.3 model."),
                            ),
                            t.div({ className: "col-lg-12" },
                                () => data.providers.length
                                    ? t.div({ className: "accordions" }, ...data.providers.map(providerAccordion))
                                    : t.div({ className: "block txt-center txt-hint p-base" }, "No providers yet."),
                            ),
                            t.div({ className: "col-lg-12" },
                                t.button({ type: "button", className: "btn secondary sm", onclick: add },
                                    t.i({ className: "ri-add-line", ariaHidden: true }), t.span({ className: "txt" }, "New provider")),
                            ),
                            t.div({ className: "col-lg-12" }, t.hr()),
                            t.div({ className: "col-lg-12" }, modelsAccordion()),
                            t.div({ className: "col-lg-12" }, t.hr()),
                            t.div({ className: "col-lg-12" },
                                t.h6({ className: "m-b-xs" }, "Limits"),
                                t.p({ className: "m-t-0 txt-hint" },
                                    "Every call passes these before it reaches a provider; one that's used up refuses the call until its day or month resets (UTC). ",
                                    "Page code counts a call for its user or guest with ", t.code(null, "$llm.for(e)"), "; plain ", t.code(null, "$llm"),
                                    " counts only toward the whole app. A call can go over by its own cost: limits are checked before and counted after."),
                                () => {
                                    const sp = data.view?.spend;
                                    return sp ? t.p({ className: "m-t-0" },
                                        `Spent today: ${money(sp.todayMicroUSD)} over ${sp.todayCalls} call${sp.todayCalls === 1 ? "" : "s"}`,
                                        sp.refusedToday ? `, ${sp.refusedToday} refused` : "",
                                        `. This month: ${money(sp.monthMicroUSD)}.`) : null;
                                },
                            ),
                            () => data.limits.some((l) => l.scope === "guests")
                                ? null
                                : t.div({ className: "col-lg-12" }, t.div({ className: "alert warning" }, t.div({ className: "content" },
                                    "Guest calls are off: a page that calls ", t.code(null, "$llm.for(e)"), " for a visitor who isn't signed in is refused until you add a limit for all guests together."))),
                            t.div({ className: "col-lg-12" }, () => data.limits.length
                                ? t.div(null, ...data.limits.map(limitRow))
                                : t.div({ className: "block txt-center txt-hint p-base" }, "No limits: calls are never refused for spending.")),
                            t.div({ className: "col-lg-12" },
                                t.button({ type: "button", className: "btn secondary sm", onclick: addLimit },
                                    t.i({ className: "ri-add-line", ariaHidden: true }), t.span({ className: "txt" }, "New limit")),
                            ),
                            () => data.message
                                ? t.div({ className: "col-lg-12" },
                                    t.div({ className: `alert ${data.message.kind}` }, t.div({ className: "content" }, data.message.text)))
                                : null,
                            t.div({ className: "col-lg-12" }, t.hr()),
                            t.div({ className: "col-lg-12" },
                                t.div({ className: "flex" },
                                    t.div({ className: "m-r-auto" }),
                                    t.button({
                                        type: "button",
                                        className: "btn transparent secondary",
                                        hidden: () => !hasChanges(),
                                        disabled: () => data.saving,
                                        onclick: () => { data.message = null; load(); },
                                    }, t.span({ className: "txt" }, "Cancel")),
                                    t.button({
                                        className: () => `btn expanded-lg ${data.saving ? "loading" : ""}`,
                                        disabled: () => data.saving || !hasChanges(),
                                    }, t.span({ className: "txt" }, "Save changes")),
                                ),
                            ),
                        );
                    },
                ),
                t.footer({ className: "page-footer" }, app.components.credits()),
            ),
        );
    });
})();
