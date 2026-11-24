// Story Time in the PocketBase dashboard's top bar, its own screen with a sidebar: Set up (the settings a step at a time,
// with a chat helper that fills the fields: /admin/assist), then the sections: the front page's title and caption, story lengths, the narrator's voice, the prompts, the pictures, the music,
// the models and the daily budget, and try each with Sample before saving. A superuser UI extension, registered from
// pb_hooks/admin_plugins.pb.js (pb_admin_ext/storytime); it calls the app's /admin/tuning and /admin/sample (superusers only), and the dashboard
// sends the signed-in superuser's token with each call.
(() => {
    // each part of the settings is its own entry in the screen's sidebar (#/storytime/<id>)
    const SECTIONS = [
        { id: "front", title: "Front page", icon: "ri-home-4-line" },
        { id: "knowledge", title: "Knowledge", icon: "ri-book-2-line" },
        { id: "length", title: "Story length", icon: "ri-timer-line" },
        { id: "mix", title: "Mix", icon: "ri-equalizer-line" },
        { id: "narrator", title: "Narrator", icon: "ri-mic-line" },
        { id: "writing", title: "Writing the story", icon: "ri-quill-pen-line" },
        { id: "pronunciation", title: "Pronunciation", icon: "ri-character-recognition-line" },
        { id: "pictures", title: "Choosing the pictures", icon: "ri-image-line" },
        { id: "music", title: "Music", icon: "ri-music-2-line" },
        { id: "inspire", title: "Inspire me", icon: "ri-lightbulb-line" },
        { id: "models", title: "Models and budget", icon: "ri-money-dollar-circle-line" },
    ];
    // Story Time in the top bar, before the last link (Settings), as other extensions' screens are
    app.store.headerLinks.splice(Math.max(app.store.headerLinks.length - 1, 0), 0, {
        href: "#/storytime",
        icon: "ri-moon-clear-line",
        label: "Story Time",
    });
    // the values being edited, kept while moving between sections so unsaved changes aren't lost; replaced on save
    let draft = null;

    const send = (path, body) => app.pb.send(path, body ? { method: "POST", body, requestKey: null } : { requestKey: null });
    const errText = (err) => err?.response?.message || err?.message || String(err);

    function sidebar() {
        return app.components.pageSidebar(
            { className: "storytime-sidebar" },
            t.nav({ className: "sidebar-content scrollable" },
                t.details(
                    { className: "nav-group", open: true },
                    t.summary({ tabIndex: -1, onfocusout: () => false, onclick: () => false, onkeyup: () => false }, "Set up"),
                    t.a(
                        { href: "#/storytime/setup", className: () => `nav-item ${app.utils.isActivePath("#/storytime/setup", false) ? "active" : ""}` },
                        t.i({ className: "ri-chat-smile-2-line", ariaHidden: true }),
                        t.span({ className: "txt" }, "Step by step (experimental)"),
                    ),
                ),
                t.details(
                    { className: "nav-group", open: true },
                    t.summary({ tabIndex: -1, onfocusout: () => false, onclick: () => false, onkeyup: () => false }, "Story Time"),
                    ...SECTIONS.map((x) => t.a(
                        { href: `#/storytime/${x.id}`, className: () => `nav-item ${app.utils.isActivePath(`#/storytime/${x.id}`, false) ? "active" : ""}` },
                        t.i({ className: x.icon, ariaHidden: true }),
                        t.span({ className: "txt" }, x.title),
                    )),
                ),
            ),
        );
    }

    function field(label, input, hint) {
        return t.div(null, t.div({ className: "field" }, t.label({}, label), input), hint ? t.div({ className: "field-help" }, hint) : null);
    }

    // #/storytime opens the first section
    app.routes.superuserOnly("#/storytime", () => page(SECTIONS[0]));
    SECTIONS.forEach((section) => app.routes.superuserOnly(`#/storytime/${section.id}`, () => page(section)));
    app.routes.superuserOnly("#/storytime/setup", () => setupPage());

    // the chat with each step's helper, kept while moving around the dashboard: step id -> [{ role, text, meta? }]
    const chats = {};

    // Set up: the settings a step at a time, each with a chat helper beside its fields (POST /admin/assist). Say what you
    // want; the helper changes just the fields you mean, and you can still edit them by hand. Next saves and moves on.
    function setupPage() {
        app.store.title = "Set up · Story Time";
        const data = store({
            loading: true,
            steps: [], // from GET /admin/assist: { id, title, question, about, fields: [{ path, label, about }] }
            at: 0, // the step shown
            busy: false, // a chat turn is out
            saving: false,
            message: null, // { kind, text }
            version: 0, // bumped when a field's value is replaced, so the fields redraw
            changed: {}, // path -> true for the fields the last turn changed, highlighted
            chatVersion: 0,
        });
        let v = null; // the values, shared with the settings sections (draft) so unsaved changes carry across
        const get = (path) => path.split(".").reduce((o, k) => (o == null ? o : o[k]), v);
        const put = (path, value) => {
            const keys = path.split("."), last = keys.pop();
            keys.reduce((o, k) => o[k], v)[last] = value;
        };
        const step = () => data.steps[data.at];
        const chat = () => {
            const s = step();
            return (chats[s.id] = chats[s.id] || [{ role: "assistant", text: s.question }]);
        };

        async function load() {
            try {
                const [tuning, assist] = await Promise.all([send("/admin/tuning"), send("/admin/assist")]);
                v = draft = draft || tuning.values;
                data.steps = assist.steps;
            } catch (err) {
                app.checkApiError(err);
                data.message = { kind: "danger", text: errText(err) };
            }
            data.loading = false;
        }

        let input = null;
        async function say() {
            const text = input.value.trim();
            if (!text || data.busy) return;
            const s = step(), c = chat();
            c.push({ role: "user", text });
            input.value = "";
            data.busy = true;
            data.chatVersion++;
            try {
                // the first line is the step's question, which the helper asked: it goes along as the helper's turn
                const out = await send("/admin/assist", { step: s.id, values: v, chat: c.map(({ role, text }) => ({ role, text })) });
                out.edits.forEach((e) => put(e.field, e.value));
                data.changed = Object.fromEntries(out.edits.map((e) => [e.field, true]));
                c.push({ role: "assistant", text: out.reply,
                    meta: [out.model, out.costUSD ? `$${out.costUSD.toFixed(4)}` : ""].filter(Boolean).join(" · ") });
                if (out.edits.length) data.version++;
            } catch (err) {
                c.push({ role: "error", text: errText(err) });
            }
            data.busy = false;
            data.chatVersion++;
            setTimeout(() => input && input.focus(), 0);
        }

        async function next() {
            data.saving = true;
            data.message = null;
            try {
                const out = await send("/admin/tuning", { values: v });
                v = draft = out.values;
                if (data.at < data.steps.length - 1) data.at++;
                else data.message = { kind: "success", text: "Saved. That's every step so far: the next ones (knowledge, research, the story) are coming." };
                data.version++;
                data.chatVersion++;
            } catch (err) {
                data.message = { kind: "danger", text: errText(err) };
            }
            data.saving = false;
        }

        function fieldsView() {
            data.version; // redraw when the helper replaces a value
            const s = step();
            return t.div({ className: "grid" }, ...s.fields.map((f) => {
                const long = f.path.startsWith("prompts.");
                const attrs = { value: get(f.path) ?? "", onchange: (e) => put(f.path, e.target.value),
                    className: data.changed[f.path] ? "storytime-changed" : "" };
                const input = f.path === "site.title"
                    ? t.input({ type: "text", ...attrs })
                    : t.textarea({ rows: long ? 18 : 3, ...attrs,
                        style: long ? "font-family: var(--monospaceFontFamily, monospace); font-size: 12px; line-height: 1.5;" : "" });
                return t.div({ className: "col-lg-12" }, field(f.label, input, data.changed[f.path] ? "Just changed by the helper." : null));
            }));
        }

        function chatView() {
            data.chatVersion; // redraw on each turn
            const lines = chat().map((m) => t.div({ className: `storytime-msg ${m.role}` },
                t.div({ className: "storytime-bubble" }, m.text),
                m.meta ? t.div({ className: "txt-hint txt-sm" }, m.meta) : null));
            if (data.busy) lines.push(t.div({ className: "storytime-msg assistant" }, t.div({ className: "storytime-bubble" }, t.span({ className: "loader sm" }))));
            const list = t.div({ className: "storytime-chat-list" }, ...lines);
            setTimeout(() => { list.scrollTop = list.scrollHeight; }, 0);
            return list;
        }

        function stepView() {
            const s = step();
            input = t.textarea({ rows: 3, placeholder: "Say what you want; Enter sends, Shift+Enter for a new line",
                onkeydown: (e) => { if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); say(); } } });
            return t.div({ className: "grid" },
                t.div({ className: "col-lg-12" },
                    t.h4({ className: "m-b-xs" }, `Step ${data.at + 1} of ${data.steps.length}: ${s.title}`),
                    t.p({ className: "txt-hint m-t-0" }, s.about, " Tell the helper what you want; it fills the fields, and you can edit them too. Nothing is saved until Next.")),
                t.div({ className: "col-lg-7" }, fieldsView),
                t.div({ className: "col-lg-5" },
                    t.div({ className: "storytime-chat" },
                        chatView,
                        input,
                        t.div({ className: "flex" },
                            t.div({ className: "m-r-auto" }),
                            t.button({ type: "button", className: () => `btn sm transparent ${data.busy ? "loading" : ""}`, disabled: () => data.busy, onclick: say },
                                t.i({ className: "ri-send-plane-line", ariaHidden: true }), t.span({ className: "txt" }, "Send"))))),
                () => data.message
                    ? t.div({ className: "col-lg-12" }, t.div({ className: `alert ${data.message.kind}` }, t.div({ className: "content" }, data.message.text)))
                    : null,
                t.div({ className: "col-lg-12" }, t.hr()),
                t.div({ className: "col-lg-12" },
                    t.div({ className: "flex" },
                        t.button({ type: "button", className: "btn transparent secondary", disabled: () => data.at === 0 || data.saving,
                            onclick: () => { data.at--; data.message = null; data.chatVersion++; } }, t.span({ className: "txt" }, "Back")),
                        t.div({ className: "m-r-auto" }),
                        t.button({ type: "button", className: () => `btn expanded-lg ${data.saving ? "loading" : ""}`, disabled: () => data.saving || data.busy,
                            onclick: next }, t.span({ className: "txt" }, "Save and next"), t.i({ className: "ri-arrow-right-line", ariaHidden: true })))),
            );
        }

        load();
        return t.div(
            { className: "page page-storytime-settings" },
            t.style(null, `
                .storytime-chat { display: flex; flex-direction: column; gap: 10px; position: sticky; top: 10px; }
                .storytime-chat-list { display: flex; flex-direction: column; gap: 10px; max-height: 60vh; overflow-y: auto; padding: 4px; }
                .storytime-msg { display: flex; flex-direction: column; max-width: 90%; }
                .storytime-msg.user { align-self: flex-end; align-items: flex-end; }
                .storytime-bubble { padding: 8px 12px; border-radius: var(--baseRadius, 8px); background: var(--baseAlt1Color); white-space: pre-wrap; }
                .storytime-msg.user .storytime-bubble { background: var(--primaryColor); color: var(--baseColor); }
                .storytime-msg.error .storytime-bubble { background: var(--dangerAltColor); color: var(--dangerColor); }
                .storytime-changed { box-shadow: 0 0 0 2px var(--successColor) inset; }
            `),
            sidebar(),
            t.div(
                { className: "page-content full-height" },
                t.header({ className: "page-header" },
                    t.nav({ className: "breadcrumbs" },
                        t.div({ className: "breadcrumb-item" }, "Story Time"),
                        t.div({ className: "breadcrumb-item" }, "Set up"))),
                t.div({ className: "wrapper m-b-base" }, () => {
                    data.at; // redraw on Next and Back
                    if (data.loading) return t.div({ className: "block txt-center" }, t.span({ className: "loader lg" }));
                    if (!v || !data.steps.length) return data.message
                        ? t.div({ className: `alert ${data.message.kind}` }, t.div({ className: "content" }, data.message.text)) : null;
                    return stepView();
                }),
                t.footer({ className: "page-footer" }, app.components.credits()),
            ),
        );
    }

    function page(section) {
        app.store.title = `${section.title} · Story Time`;
        const data = store({
            loading: true,
            saving: false,
            message: null, // { kind, text }
            voices: [],
            voiceChoice: "", // the voice picked on the page, for its help text
            clips: [], // recordings a clone can imitate: { id, name, passage, created, file }
            buckets: [], // LLM Search's buckets, for Knowledge: { name, title, documents, chunks }
            stories: null, // the stories bucket: { name, documents }
            remembering: null, // Add told stories: { busy } or { text } or { error }
            clipChoice: "", // the recording picked on the page
            fileToken: "", // lets the superuser's browser play a recording (superusers only)
            levelling: null, // Compress and level on a recording: { id, busy } or { id, text } or { id, error }
            knowledge: [], // the buckets ticked on the page
            check: null, // the last check of a recording: { id, busy } or { id, heard, match, missing, cutOff, model } or { id, error }
            rec: { state: "idle" }, // the recorder: idle, recording, saving, saved (seconds, name); error
            spent: 0,
            estimate: null, // what a story costs with the models on the page: { minutes, pictures, usd, complete, parts }
            estimating: false,
            version: 0, // bumped when the values are replaced, so the form redraws with them
            samples: {}, // what -> { busy, meta, error, audio, image, idea, story }
        });
        // the values being edited: a plain object, so typing doesn't redraw the form
        let v = null;
        const extra = { storyTopic: "", pictureStyle: "Muted oil painting, soft light, deep blues and ambers",
            musicPrompt: "Quiet ambient music on soft piano and strings" };

        const get = (path) => path.split(".").reduce((o, k) => (o == null ? o : o[k]), v)
        const put = (path, value) => {
            const keys = path.split("."), last = keys.pop();
            keys.reduce((o, k) => o[k], v)[last] = value;
        };

        // inputs bound to a key of the values ("voice.seed"); numbers empty mean "not set" (null)
        const text = (path, attrs = {}) => t.input({ type: "text", value: get(path) ?? "", onchange: (e) => put(path, e.target.value), ...attrs });
        const num = (path, attrs = {}) => t.input({ type: "number", value: get(path) ?? "",
            onchange: (e) => put(path, e.target.value === "" ? null : Number(e.target.value)), ...attrs });
        const slider = (path, min, max, step, show) => {
            const label = t.span({ className: "txt-hint", style: "min-width: 7em; text-align: right;" }, show(get(path)));
            return t.div({ className: "flex", style: "align-items: center; gap: 10px; padding: 6px 0;" },
                t.input({ type: "range", min, max, step, value: get(path), style: "flex: 1;",
                    oninput: (e) => { put(path, Number(e.target.value)); label.textContent = show(Number(e.target.value)); } }),
                label);
        };
        const area = (path, rows = 4) => t.textarea({ rows, value: get(path) ?? "", onchange: (e) => put(path, e.target.value),
            style: "font-family: var(--monospaceFontFamily, monospace); font-size: 12px; line-height: 1.5;" });

        // a model field: a searchable list of the kind's models with their prices and what each would cost for this
        // field's part of a story, cheapest first (ext/llm's dashboard helper), or a plain box when that isn't loaded. A
        // change works out the story's cost again.
        const modelField = (path, kind) => window.pocketone?.llm
            ? window.pocketone.llm.select({ kind, value: () => get(path), onchange: (id) => { put(path, id); estimateSoon(); },
                // each model also shows what its part of a story costs on it (the work from the estimate's jobs)
                job: () => data.estimate?.jobs?.[path]?.meters || null, per: "a story" })
            : t.input({ type: "text", value: get(path) ?? "", onchange: (e) => { put(path, e.target.value); estimateSoon(); } });

        // estimateSoon asks again what a story costs with the models on the page (unsaved), a moment after the last change
        let estimateTimer = 0;
        function estimateSoon() {
            clearTimeout(estimateTimer);
            estimateTimer = setTimeout(async () => {
                data.estimating = true;
                try {
                    data.estimate = await send("/admin/estimate", { values: v });
                } catch (err) {
                    data.estimate = { error: errText(err) };
                }
                data.estimating = false;
            }, 300);
        }

        // estimateView is the story's cost, step by step, each saying how it was worked out
        function estimateView() {
            const e = data.estimate;
            if (!e) return null;
            if (e.error) return t.div({ className: "txt-danger txt-sm" }, e.error);
            const usd = (n) => window.pocketone?.llm ? window.pocketone.llm.usd(n) : "$" + n.toFixed(3);
            const how = (p) => p.method === "stories" ? `average of ${p.stories} stor${p.stories === 1 ? "y" : "ies"} made with it`
                : p.method === "listed" ? `list price × ${p.note}` : "not known: no stories with this model yet, and it lists no price";
            return t.div({ className: () => (data.estimating ? "faded" : "") },
                t.p({ className: "m-b-xs" }, t.strong(null, `A story costs about ${usd(e.usd)}`),
                    ` (${e.minutes} min, ${e.pictures} picture${e.pictures === 1 ? "" : "s"})`,
                    e.complete ? "." : ", plus the steps whose cost isn't known."),
                t.table({ className: "table txt-sm" }, t.tbody(null, ...e.parts.map((p) => t.tr(null,
                    t.td({ className: "min-width" }, p.step),
                    t.td({ className: "min-width" }, p.usd === null ? "?" : usd(p.usd)),
                    t.td({ className: "txt-hint" }, how(p)))))),
            );
        }

        async function load() {
            try {
                const out = await send("/admin/tuning");
                const kept = !!draft;
                v = draft = draft || out.values;
                data.voices = out.voices || [];
                data.clips = out.clips || [];
                data.buckets = out.buckets || [];
                data.stories = out.stories || null;
                data.knowledge = [...((v.knowledge && v.knowledge.buckets) || [])];
                data.spent = out.spentToday || 0;
                data.estimate = out.estimate || null;
                if (kept) estimateSoon(); // the saved estimate doesn't know the unsaved changes
                data.version++;
            } catch (err) {
                app.checkApiError(err);
                data.message = { kind: "danger", text: errText(err) };
            }
            data.loading = false;
        }

        async function save(reset) {
            data.saving = true;
            data.message = null;
            try {
                const out = await send("/admin/tuning", reset ? { reset: true } : { values: v });
                v = draft = out.values;
                data.knowledge = [...((v.knowledge && v.knowledge.buckets) || [])];
                data.version++;
                data.message = { kind: "success", text: reset ? "Defaults put back." : "Saved. New stories use it." };
            } catch (err) {
                data.message = { kind: "danger", text: errText(err) };
            }
            data.saving = false;
        }

        // Export: the saved tuning and every recording, as one JSON file to import on another copy of the app (dev to
        // prod). Import: that file; recordings already here are kept, and the tuning is saved.
        async function exportJSON() {
            data.message = null;
            try {
                const out = await send("/admin/export");
                const a = document.createElement("a");
                a.href = URL.createObjectURL(new Blob([JSON.stringify(out, null, 2)], { type: "application/json" }));
                a.download = `storytime-${out.exported.slice(0, 10)}.json`;
                a.click();
                setTimeout(() => URL.revokeObjectURL(a.href), 10000);
                data.message = { kind: "success", text: `Exported the saved settings and ${out.voices.length} recording${out.voices.length === 1 ? "" : "s"}. Unsaved changes on this page aren't in it.` };
            } catch (err) {
                data.message = { kind: "danger", text: errText(err) };
            }
        }

        function importJSON() {
            const input = document.createElement("input");
            input.type = "file";
            input.accept = "application/json,.json";
            input.onchange = async () => {
                const file = input.files && input.files[0];
                if (!file) return;
                if (!confirm(`Import ${file.name}? It replaces the settings on this page (saved) and adds its recordings.`)) return;
                data.saving = true;
                data.message = null;
                try {
                    const out = await send("/admin/import", JSON.parse(await file.text()));
                    v = draft = out.values;
                    data.knowledge = [...((v.knowledge && v.knowledge.buckets) || [])];
                    await refreshClips();
                    data.clipChoice = "";
                    data.voiceChoice = "";
                    data.version++;
                    data.message = { kind: "success", text: `Imported and saved. Recordings: ${out.voices.added} added, ${out.voices.kept} already here.` };
                } catch (err) {
                    data.message = { kind: "danger", text: err instanceof SyntaxError ? "That file isn't JSON." : errText(err) };
                }
                data.saving = false;
            };
            input.click();
        }

        // sample tries one piece with the values as they are on the page, saved or not
        async function sample(what, prompt) {
            data.samples = { ...data.samples, [what]: { busy: true } };
            let s;
            try {
                const out = await send("/admin/sample", { what, values: v, prompt });
                s = { ...out, meta: [out.model, out.seconds != null ? `${out.seconds} s` : "", out.costUSD ? `$${out.costUSD.toFixed(4)}` : ""]
                    .filter(Boolean).join(" · ") };
            } catch (err) {
                s = { error: errText(err) };
            }
            data.samples = { ...data.samples, [what]: s };
        }

        function sampleRow(what, label, prompt) {
            return t.div({ className: "col-lg-12" },
                t.div({ className: "flex" },
                    t.button({
                        type: "button",
                        className: () => `btn sm secondary ${data.samples[what]?.busy ? "loading" : ""}`,
                        disabled: () => !!data.samples[what]?.busy,
                        onclick: () => sample(what, prompt ? prompt() : ""),
                    }, t.i({ className: "ri-play-circle-line", ariaHidden: true }), t.span({ className: "txt" }, label)),
                    t.span({ className: "txt-hint" }, () => data.samples[what]?.meta || ""),
                ),
                () => {
                    const s = data.samples[what];
                    if (!s || s.busy) return null;
                    if (s.error) return t.div({ className: "alert danger m-t-sm m-b-0" }, t.div({ className: "content" }, s.error));
                    if (s.audio) return t.audio({ controls: true, autoplay: true, src: s.audio, style: "width: 100%; margin-top: 10px;" });
                    if (s.image) return t.img({ src: s.image, style: "width: 100%; margin-top: 10px; border-radius: var(--baseRadius, 6px);" });
                    if (s.idea) {
                        const ins = s.inspiration || {};
                        return t.div({ className: "m-t-sm" },
                            t.p({ className: "m-b-xs" }, t.strong(null, s.idea), ins.why ? t.span({ className: "txt-hint" }, ` · ${ins.why}`) : ""),
                            ...(ins.candidates || []).map((c) => t.p({ className: "m-b-xs txt-hint" },
                                `${c.topic === s.idea ? "✓" : "·"} ${c.topic}: closest told ${c.nearest.length ? c.nearest.join("; ") : "none"}`)),
                            ins.material ? t.p({ className: "txt-hint m-b-0" }, "Drawn from: ", [...new Set(ins.material)].join(", ")) : null);
                    }
                    if (s.story) {
                        return t.div({ className: "m-t-sm" },
                            t.p({ className: "m-b-xs" }, t.strong(null, s.story.title), " · ", s.story.topic),
                            ...s.story.sentences.map((x) => t.p({ className: "m-b-xs" }, x)),
                            t.p({ className: "txt-hint m-b-0" }, "Pictures: ", s.story.look),
                            s.story.say && s.story.say.length
                                ? t.p({ className: "txt-hint m-b-0" }, "Said as: ", s.story.say.map((x) => `${x.term} → "${x.spoken}"`).join(", "))
                                : null);
                    }
                    return null;
                },
            );
        }

        // block is one section's fields; only the open section's is drawn
        function block(title, about, ...children) {
            if (title !== section.title) return null;
            return t.div(null,
                about ? t.p({ className: "m-t-0 txt-hint" }, about) : null,
                t.div({ className: "grid" }, ...children));
        }

        // what each of auk's voices does (Vultr's own descriptions)
        function voiceHelp(id) {
            if (id === "clone") return "Reads in the voice of one of your recordings, below. The delivery comes from how the recording sounds, so Instructions don't apply; speed and seed still do.";
            if (id === "design") return "A new voice made from the description in Instructions (required). The same seed gives the same voice.";
            if (id === "default") return "A calm middle-aged woman with a low, clear voice, generated afresh each time: set a seed to keep it the same. Instructions change how she reads, not who she is.";
            const x = data.voices.find((y) => y.id === id);
            return x && x.description ? x.description : "";
        }

        function voiceOptions() {
            const current = get("voice.voice") || "default";
            const clones = data.voices.some((x) => x.mode === "clone") || current === "clone";
            const ids = new Set(["default", ...data.voices.filter((x) => x.mode !== "clone" && x.id !== "clone").map((x) => x.id), current]);
            ids.delete("clone");
            const out = [...ids].map((id) => {
                const x = data.voices.find((y) => y.id === id) || { id };
                return { value: id, label: x.name ? `${x.name} (${id})` : id };
            });
            if (clones) out.push({ value: "clone", label: "A recording of a real voice (clone)" });
            return out;
        }

        // ---- the clone's recordings: pick one, play it, record a new one in the browser, delete one
        const PASSAGE = "It's late, and the house is quiet. Somewhere a server hums along, answering requests one at a time, " +
            "never in a hurry. Let's slow down too. Take a breath. Tonight I'm going to tell you a story, nice and easy, and you " +
            "don't have to remember any of it. Just listen, and let the day go.";
        let recorder = null;
        const cloneOn = () => (data.voiceChoice || get("voice.voice")) === "clone";
        const clipURL = (c) => `/api/files/voices/${c.id}/${encodeURIComponent(c.file)}?token=${data.fileToken}`;

        async function refreshClips() {
            const list = await app.pb.collection("voices").getFullList({ sort: "-created", requestKey: null });
            data.clips = list.map((r) => ({ id: r.id, name: r.name, passage: r.passage, created: r.created, file: r.audio }));
        }

        function pickClip(id) {
            put("voice.clip", id);
            data.clipChoice = id;
        }

        // A take is saved as soon as it stops and played back from the server: the dashboard's Content-Security-Policy
        // blocks playing blob: and data: audio, so an unsaved take can't be heard here. Delete a take that isn't right.
        let takeName = "My voice";
        // A voice take straight off a microphone is quiet and uneven, and a clone copies its level, so every take is
        // compressed and levelled before it's saved: a gentle compressor (3:1) evens out loud and soft words, then makeup
        // gain brings it to TAKE_DB with peaks kept under -1 dBFS. Saved as 24 kHz mono 16-bit WAV.
        const TAKE_DB = -16, RATE = 24000;
        function measure(x) {
            const win = Math.round(RATE * 0.05), dbs = [];
            let peak = 0;
            for (let i = 0; i + win <= x.length; i += win) {
                let sum = 0;
                for (let j = i; j < i + win; j++) { sum += x[j] * x[j]; peak = Math.max(peak, Math.abs(x[j])); }
                const db = 10 * Math.log10(sum / win + 1e-12);
                if (db > -50) dbs.push(db);
            }
            dbs.sort((a, b) => a - b);
            return { db: dbs.length ? dbs[dbs.length >> 1] : null, peak };
        }
        function wav(x) {
            const out = new DataView(new ArrayBuffer(44 + x.length * 2));
            const str = (o, v) => [...v].forEach((c, i) => out.setUint8(o + i, c.charCodeAt(0)));
            str(0, "RIFF"); out.setUint32(4, 36 + x.length * 2, true); str(8, "WAVEfmt ");
            out.setUint32(16, 16, true); out.setUint16(20, 1, true); out.setUint16(22, 1, true);
            out.setUint32(24, RATE, true); out.setUint32(28, RATE * 2, true); out.setUint16(32, 2, true); out.setUint16(34, 16, true);
            str(36, "data"); out.setUint32(40, x.length * 2, true);
            x.forEach((v, i) => out.setInt16(44 + i * 2, Math.max(-1, Math.min(1, v)) * 0x7fff, true));
            return new Blob([out], { type: "audio/wav" });
        }
        const dbText = (m) => (m.db === null ? "silent" : `${Math.round(m.db)} dBFS`);
        async function compressAndLevel(buffer) {
            const decoded = await new OfflineAudioContext(1, 1, RATE).decodeAudioData(buffer);
            const before = measure(decoded.getChannelData(0));
            if (before.db === null) throw new Error("the recording is silent");
            const ctx = new OfflineAudioContext(1, decoded.length, RATE);
            const src = ctx.createBufferSource();
            src.buffer = decoded;
            const pre = ctx.createGain();
            pre.gain.value = Math.max(0.1, Math.min(40, Math.pow(10, (-24 - before.db) / 20))); // into the compressor at -24
            const comp = ctx.createDynamicsCompressor();
            comp.threshold.value = -30; comp.knee.value = 10; comp.ratio.value = 3; comp.attack.value = 0.01; comp.release.value = 0.25;
            src.connect(pre).connect(comp).connect(ctx.destination);
            src.start();
            const compressed = await ctx.startRendering();
            const mid = measure(compressed.getChannelData(0));
            // makeup gain to TAKE_DB, then a fast limiter holds the word attacks the compressor let through
            const ctx2 = new OfflineAudioContext(1, compressed.length, RATE);
            const src2 = ctx2.createBufferSource();
            src2.buffer = compressed;
            const makeup = ctx2.createGain();
            makeup.gain.value = Math.pow(10, (TAKE_DB - mid.db) / 20);
            const lim = ctx2.createDynamicsCompressor();
            lim.threshold.value = -6; lim.knee.value = 0; lim.ratio.value = 20; lim.attack.value = 0.001; lim.release.value = 0.05;
            src2.connect(makeup).connect(lim).connect(ctx2.destination);
            src2.start();
            const y = (await ctx2.startRendering()).getChannelData(0);
            // Web Audio's compressor adds its own makeup gain, so trim to TAKE_DB as measured, nothing above -1 dBFS
            const m = measure(y);
            const trim = Math.min(Math.pow(10, (TAKE_DB - m.db) / 20), 0.89 / Math.max(m.peak, 1e-6));
            for (let i = 0; i < y.length; i++) y[i] *= trim;
            return { blob: wav(y), before, after: measure(y) };
        }

        async function startRecording() {
            try {
                const stream = await navigator.mediaDevices.getUserMedia({ audio: { echoCancellation: false, noiseSuppression: true } });
                const chunks = [];
                const started = Date.now();
                recorder = new MediaRecorder(stream);
                recorder.ondataavailable = (e) => e.data.size && chunks.push(e.data);
                recorder.onstop = () => {
                    stream.getTracks().forEach((x) => x.stop());
                    saveTake(new Blob(chunks, { type: recorder.mimeType || "audio/webm" }), Math.round((Date.now() - started) / 1000));
                };
                recorder.start();
                data.rec = { state: "recording" };
            } catch (err) {
                data.rec = { state: "idle", error: "The browser didn't give the microphone: " + errText(err) };
            }
        }

        async function saveTake(blob, seconds) {
            data.rec = { state: "saving", seconds };
            try {
                let file, levelled = "";
                try {
                    const done = await compressAndLevel(await blob.arrayBuffer());
                    file = new File([done.blob], "voice.wav", { type: "audio/wav" });
                    levelled = `compressed and levelled from ${dbText(done.before)} to ${dbText(done.after)}`;
                } catch (err) {
                    // the browser couldn't decode it: keep the take as recorded
                    const ext = blob.type.includes("mp4") ? "m4a" : blob.type.includes("ogg") ? "ogg" : blob.type.includes("wav") ? "wav" : "webm";
                    file = new File([blob], `voice.${ext}`, { type: blob.type.split(";")[0] });
                    levelled = "saved as recorded (couldn't level it: " + errText(err) + ")";
                }
                const form = new FormData();
                form.append("name", takeName.trim() || "My voice");
                form.append("passage", PASSAGE);
                form.append("audio", file);
                const saved = await app.pb.collection("voices").create(form, { requestKey: null });
                await refreshClips();
                pickClip(saved.id);
                data.rec = { state: "saved", seconds, name: saved.name, levelled };
                checkClip(saved.id);
            } catch (err) {
                data.rec = { state: "idle", error: "The take wasn't saved: " + errText(err) };
            }
        }

        // check: transcribe a recording and compare it with its passage (a stumble, a skipped line, a cut-off ending)
        async function checkClip(id) {
            data.check = { id, busy: true };
            try {
                const out = await send("/admin/check", { clip: id });
                data.check = { id, ...out };
            } catch (err) {
                data.check = { id, error: errText(err) };
            }
        }

        function checkView(id) {
            const c = data.check;
            if (!c || c.id !== id) return null;
            if (c.busy) return t.p({ className: "txt-hint m-t-sm m-b-0" }, "Checking the recording against the passage…");
            if (c.error) return t.div({ className: "alert danger m-t-sm m-b-0" }, t.div({ className: "content" }, c.error));
            const pct = Math.round(c.match * 100);
            const kind = pct >= 90 && !c.cutOff ? "success" : pct >= 70 ? "warning" : "danger";
            const missing = c.missing.length ? ` Missing or misheard: ${c.missing.slice(0, 12).join(", ")}${c.missing.length > 12 ? "…" : ""}.` : "";
            return t.div({ className: `alert ${kind} m-t-sm m-b-0` }, t.div({ className: "content" },
                t.p({ className: "m-b-xs" }, t.strong(null, `${pct}% of the passage heard.`), c.cutOff ? " It ends before the passage does." : "", missing,
                    t.span({ className: "txt-hint" }, ` (${c.model}, ${c.seconds} s)`)),
                t.p({ className: "m-b-0 txt-hint" }, "Heard: ", c.heard)));
        }

        // compress and level a recording already saved (one recorded before this was built), in place: same id, so the
        // narrator keeps pointing at it
        async function levelClip(clip) {
            data.levelling = { id: clip.id, busy: true };
            try {
                const done = await compressAndLevel(await (await fetch(clipURL(clip))).arrayBuffer());
                const form = new FormData();
                form.append("audio", new File([done.blob], "voice.wav", { type: "audio/wav" }));
                await app.pb.collection("voices").update(clip.id, form, { requestKey: null });
                await refreshClips();
                data.levelling = { id: clip.id, text: `Compressed and levelled from ${dbText(done.before)} to ${dbText(done.after)}. Play it above; new stories use it.` };
            } catch (err) {
                data.levelling = { id: clip.id, error: errText(err) };
            }
        }

        // Knowledge: which buckets the stories know (tick boxes), and the stories bucket kept in step with what's told
        function toggleBucket(name, on) {
            const list = (data.knowledge || []).filter((n) => n !== name);
            if (on) list.push(name);
            data.knowledge = list;
            put("knowledge.buckets", list);
        }

        async function rememberAll() {
            data.remembering = { busy: true };
            try {
                const out = await send("/admin/stories", {});
                const t2 = await send("/admin/tuning");
                data.stories = t2.stories;
                data.remembering = { text: out.added ? `Added ${out.added} told ${out.added === 1 ? "story" : "stories"}.` : "Every told story is already in it." };
            } catch (err) {
                data.remembering = { error: errText(err) };
            }
        }

        function knowledgeBlock() {
            return block("Knowledge", "What the stories know. Tick the LLM Search buckets they draw on: the writer is given the best passages for each story's topic, and Inspire me picks topics from them that haven't been told yet. None ticked: the models' own knowledge.",
                t.div({ className: "col-lg-12" }, () => {
                    if (!data.buckets.length) {
                        return t.p({ className: "txt-hint m-0" }, "No buckets yet. ", t.a({ href: "#/search" }, "Make one on the Search screen"),
                            " (a Git repository, docs, files), then come back and tick it.");
                    }
                    return t.div(null, ...data.buckets.map((b) => t.div({ className: "field form-field-toggle", style: "margin: 4px 0;" },
                        t.input({ type: "checkbox", id: `kb-${b.name}`, checked: (data.knowledge || []).includes(b.name),
                            onchange: (e) => toggleBucket(b.name, e.target.checked) }),
                        t.label({ htmlFor: `kb-${b.name}` }, `${b.title || b.name} `, t.span({ className: "txt-hint" }, `(${b.name}: ${b.documents} documents)`)))),
                        t.p({ className: "txt-hint m-t-sm m-b-0" }, t.a({ href: "#/search" }, "Manage buckets on the Search screen"), "."));
                }),
                t.div({ className: "col-lg-12" }, field("Skip", t.textarea({ rows: 3, value: (get("knowledge.skip") || []).join("\n"),
                    onchange: (e) => put("knowledge.skip", e.target.value.split("\n").map((x) => x.trim()).filter(Boolean)) }),
                    "Documents not worth a story, one pattern per line (go.mod, *.lock, docs/**/toc.md). * is anything but /, ** anything; a pattern without / matches the file name.")),
                t.div({ className: "col-lg-12" }, () => t.div({ className: "flex", style: "align-items: center; gap: 12px;" },
                    t.span({ className: "txt-hint" }, `Stories told, as a bucket (${data.stories ? data.stories.name : "storytime-stories"}): `,
                        data.stories ? `${data.stories.documents} stories` : "not made yet",
                        ". Each finished story goes in by itself; Inspire me checks candidates against it."),
                    t.button({ type: "button", className: () => `btn sm transparent secondary ${data.remembering?.busy ? "loading" : ""}`,
                        disabled: () => !!data.remembering?.busy, onclick: rememberAll }, t.span({ className: "txt" }, "Add told stories")))),
                t.div({ className: "col-lg-12" }, () => {
                    const r = data.remembering;
                    if (!r || r.busy) return null;
                    return t.div({ className: `alert ${r.error ? "danger" : "success"} m-t-sm m-b-0` }, t.div({ className: "content" }, r.error || r.text));
                }),
            );
        }

        async function deleteClip(id) {
            if (!confirm("Delete this recording? Stories already made keep their narration.")) return;
            await app.pb.collection("voices").delete(id, { requestKey: null });
            if ((data.clipChoice || get("voice.clip")) === id) pickClip("");
            await refreshClips();
        }

        function clonePanel() {
            const chosen = data.clipChoice || get("voice.clip");
            const clip = data.clips.find((c) => c.id === chosen);
            const r = data.rec;
            return t.div({ className: "col-lg-12" },
                t.div({ className: "grid" },
                    t.div({ className: "col-md-6" }, field("Recording",
                        t.select({ onchange: (e) => pickClip(e.target.value) },
                            t.option({ value: "", selected: !clip }, data.clips.length ? "Pick a recording" : "No recordings yet: record one"),
                            ...data.clips.map((c) => t.option({ value: c.id, selected: c.id === chosen }, `${c.name} (${c.created.slice(0, 10)})`))),
                        "Sent with every story, so keep it to 10 to 30 seconds. Only superusers can hear it here.")),
                    t.div({ className: "col-md-6" }, clip
                        ? t.div({ className: "flex", style: "margin-top: 26px;" },
                            t.audio({ controls: true, src: clipURL(clip), style: "flex: 1;" }),
                            t.button({ type: "button", className: () => `btn sm transparent secondary ${data.levelling?.busy ? "loading" : ""}`,
                                disabled: () => !!data.levelling?.busy, onclick: () => levelClip(clip) },
                                t.span({ className: "txt" }, "Compress and level")),
                            t.button({ type: "button", className: () => `btn sm transparent secondary ${data.check?.busy ? "loading" : ""}`,
                                disabled: () => !!data.check?.busy, onclick: () => checkClip(clip.id) },
                                t.span({ className: "txt" }, "Check")),
                            t.button({ type: "button", className: "btn sm transparent danger", onclick: () => deleteClip(clip.id) },
                                t.span({ className: "txt" }, "Delete")))
                        : null),
                    clip ? t.div({ className: "col-lg-12" }, () => {
                        const l = data.levelling;
                        if (!l || l.id !== clip.id || l.busy) return null;
                        return t.div({ className: `alert ${l.error ? "danger" : "success"} m-t-sm m-b-0` }, t.div({ className: "content" }, l.error || l.text));
                    }) : null,
                    clip ? t.div({ className: "col-lg-12" }, () => checkView(clip.id)) : null,
                    t.div({ className: "col-lg-12" },
                        t.p({ className: "m-b-xs" }, t.strong(null, "Record a new one. "),
                            "Read this slowly and quietly, the way you want the stories told. A quiet room and a steady distance from the microphone help more than a good microphone."),
                        t.blockquote({ className: "m-t-0", style: "font-size: 1.1em; line-height: 1.6;" }, PASSAGE),
                        t.div({ className: "flex" },
                            t.div({ className: "field", style: "max-width: 240px; margin: 0;" },
                                t.label({}, "Name"),
                                t.input({ type: "text", placeholder: "My voice", value: takeName,
                                    disabled: r.state === "recording" || r.state === "saving", oninput: (e) => { takeName = e.target.value } })),
                            r.state === "recording"
                                ? t.button({ type: "button", className: "btn sm danger", onclick: () => recorder && recorder.stop() },
                                    t.i({ className: "ri-stop-circle-line", ariaHidden: true }), t.span({ className: "txt" }, "Stop and save"))
                                : t.button({ type: "button", className: () => `btn sm secondary ${data.rec.state === "saving" ? "loading" : ""}`,
                                    disabled: r.state === "saving", onclick: startRecording },
                                    t.i({ className: "ri-mic-line", ariaHidden: true }), t.span({ className: "txt" }, r.state === "saved" ? "Record another" : "Record")),
                            r.state === "recording" ? t.span({ className: "txt-hint" }, "Recording… read the passage, then Stop and save.") : null,
                            r.state === "saving" ? t.span({ className: "txt-hint" }, `Saving ${r.seconds} s…`) : null),
                        r.state === "saved"
                            ? t.div({ className: `alert ${r.seconds < 8 ? "warning" : "success"} m-t-sm m-b-0` }, t.div({ className: "content" },
                                `Saved "${r.name}" (${r.seconds} s, ${r.levelled}) and picked it above: play it there. `,
                                r.seconds < 8 ? "That's short for a clone; 15 to 30 seconds matches the voice better. " : "",
                                "If it isn't right, Delete it and record another. Save changes to make it the narrator."))
                            : null,
                        r.error ? t.div({ className: "alert danger m-t-sm m-b-0" }, t.div({ className: "content" }, r.error)) : null,
                    ),
                ));
        }

        function form() {
            return t.div({ className: "grid" },
                t.div({ className: "col-lg-12" }, t.p({ className: "m-t-0" },
                    "How ", t.a({ href: "/", target: "_blank" }, "Story Time"), " makes its stories. Sample tries a piece with what's on these pages, ",
                    "saved or not; new stories use what's saved. Changes carry across the sections until you save.")),
                t.div({ className: "col-lg-12" },
                    block("Front page", "What the front page says. The title's last word is highlighted.",
                        t.div({ className: "col-md-6" }, field("Title", text("site.title"))),
                        t.div({ className: "col-md-6" }, field("Prompt placeholder", text("site.placeholder"))),
                        t.div({ className: "col-lg-12" }, field("Caption", area("site.caption", 3), "Under the title: what the app tells and how to ask.")),
                        t.div({ className: "col-lg-12" }, field("Note", text("site.note"), "Beside the buttons, until a story starts.")),
                    ),
                    knowledgeBlock(),
                    block("Story length", "Listeners pick a length on a slider from Short to Long. Pictures follow from the length.",
                        t.div({ className: "col-md-4" }, field("Shortest, minutes", num("length.minMinutes", { min: "1", max: "30", step: "1" }), "The slider's Short end.")),
                        t.div({ className: "col-md-4" }, field("Longest, minutes", num("length.maxMinutes", { min: "1", max: "30", step: "1" }), "The slider's Long end. The same as Shortest hides the slider.")),
                        t.div({ className: "col-md-4" }, field("Starts at, minutes", num("length.defaultMinutes", { min: "1", max: "30", step: "1" }), "Where the slider starts.")),
                        t.div({ className: "col-md-4" }, field("Words per minute", num("length.wordsPerMinute", { min: "60", max: "250", step: "5" }), "The narrator's pace, which sets how many words a story gets. Measure it from a told story: its words divided by its spoken minutes.")),
                        t.div({ className: "col-md-4" }, field("Pictures per minute", num("length.picturesPerMinute", { min: "0.1", max: "10", step: "0.5" }), "A 2-minute story at 1.5 gets 3 pictures.")),
                        t.div({ className: "col-md-4" }, field("Most pictures", num("length.maxPictures", { min: "1", max: "20", step: "1" }), "However long the story.")),
                    ),
                    block("Mix", "How every story sounds when it plays. The player measures each story's voice and music once and sets them to these, so old and new stories, generated and cloned voices, all sound the same. Sample a story after saving to hear it.",
                        t.div({ className: "col-lg-12" }, field("Loudness: quieter to louder",
                            slider("mix.voiceDb", "-36", "-12", "1", (v) => `voice ${v} dBFS`),
                            "The voice's level. -24 is close to broadcast loudness; online videos are often around -14, much louder.")),
                        t.div({ className: "col-lg-12" }, field("Music to voice",
                            slider("mix.musicUnderDb", "0", "30", "1", (v) => (v === 0 ? "music as loud" : `music ${v} dB under`)),
                            "Left brings the music up to the voice; right tucks it further under. 18 dB is quiet underneath.")),
                    ),
                    block("Narrator", "The voice that reads every story in a language the speech model speaks. \"design\" makes a voice from the instructions; the same seed makes the same voice each time.",
                        // a plain <select>: the dashboard's own dropdown didn't keep the choice here
                        t.div({ className: "col-md-4" }, field("Voice",
                            t.select({ onchange: (e) => { put("voice.voice", e.target.value); data.voiceChoice = e.target.value } },
                                ...voiceOptions().map((o) => t.option({ value: o.value, selected: o.value === (get("voice.voice") || "default") }, o.label))),
                            () => voiceHelp(data.voiceChoice || get("voice.voice") || "default"))),
                        () => (cloneOn() ? clonePanel() : null),
                        t.div({ className: "col-md-4" }, field("Seed", num("voice.seed"), "Empty: a little different each time.")),
                        t.div({ className: "col-md-4" }, field("Speed", num("voice.speed", { step: "0.05", min: "0.5", max: "2" }), "Empty: normal (1).")),
                        t.div({ className: "col-lg-12" }, () => cloneOn()
                            ? field("Instructions", t.textarea({ rows: 3, disabled: true, value: "" }), "Not used by a cloned voice: its delivery comes from the recording.")
                            : field("Instructions", area("voice.instructions", 3),
                                "How to read, or with design, the voice itself: \"A deep, calm older man's voice, slow and soft.\"")),
                        t.div({ className: "col-lg-12" }, field("Sample text", area("voice.sample", 3))),
                        sampleRow("voice", "Sample the voice"),
                    ),
                    block("Writing the story", "What the stories are like: who tells them, about what, the tone. The app adds the rest after it: the length, what to do when the listener leaves the topic to it, that every sentence is read aloud, the answer's fields, and the words the narrator could say wrong (say).",
                        t.div({ className: "col-lg-12" }, field("Instructions", area("prompts.write", 16))),
                        t.div({ className: "col-lg-12" }, field("Topic for the sample",
                            t.input({ type: "text", placeholder: "Empty: it picks", value: extra.storyTopic, onchange: (e) => (extra.storyTopic = e.target.value) }))),
                        sampleRow("story", "Sample a 1-minute story", () => extra.storyTopic),
                    ),
                    block("Pronunciation", "How words in this field are said, with examples. A cloned voice takes no instructions, so the writer lists the words a narrator could get wrong with a respelling, odd-looking words it left out (acronyms, names with capitals inside) are asked about separately, and the voice reads the respelled text while the captions keep the written words.",
                        t.div({ className: "col-lg-12" }, field("How words are said", area("prompts.pronounce", 5))),
                    ),
                    block("Choosing the pictures", "How the timed transcript becomes pictures, their style and the music prompt. Placeholder: {n}. Keep the field names (style, music, pictures, line, prompt).",
                        t.div({ className: "col-lg-12" }, field("Instructions", area("prompts.place", 10))),
                        t.div({ className: "col-lg-12" }, field("Added after every picture's prompt", text("picture"))),
                        t.div({ className: "col-lg-12" }, field("Style for the sample picture",
                            t.input({ type: "text", value: extra.pictureStyle, onchange: (e) => (extra.pictureStyle = e.target.value) }))),
                        sampleRow("picture", "Sample a picture", () => extra.pictureStyle),
                    ),
                    block("Music", null,
                        t.div({ className: "col-md-8" }, field("Added after every story's music prompt", text("music.suffix"))),
                        t.div({ className: "col-md-4" }, field("Seconds", num("music.seconds", { min: "10", max: "240" }), "The song loops.")),
                        t.div({ className: "col-lg-12" }, field("Music prompt for the sample",
                            t.input({ type: "text", value: extra.musicPrompt, onchange: (e) => (extra.musicPrompt = e.target.value) }))),
                        sampleRow("music", "Sample 20 seconds", () => extra.musicPrompt),
                    ),
                    block("Inspire me", "Instructions set the spirit of every idea. With knowledge buckets, Inspire me also draws a few random passages, one per thing they belong to, and turns each into a story request with the second prompt (placeholder: {count}); the answer's shape (subject, prompt) is fixed.",
                        t.div({ className: "col-lg-12" }, field("Instructions", area("prompts.inspire", 5))),
                        t.div({ className: "col-lg-12" }, field("Ideas from knowledge", area("prompts.ideas", 6))),
                        sampleRow("idea", "Sample an idea"),
                    ),
                    block("Models and budget", "Each from the providers in Settings → LLM, cheapest first, with its price: listed by the provider, or measured from what this app's calls were billed.",
                        t.div({ className: "col-md-6" }, field("Writing", modelField("models.text", "text"))),
                        t.div({ className: "col-md-6" }, field("Picking the story", modelField("models.idea", "text"))),
                        t.div({ className: "col-md-6" }, field("Voice", modelField("models.speech", "speech"))),
                        t.div({ className: "col-md-6" }, field("Voice, other languages", modelField("models.speechOther", "speech"))),
                        t.div({ className: "col-md-6" }, field("Word timing", modelField("models.align", "align"))),
                        t.div({ className: "col-md-6" }, field("Pictures", modelField("models.image", "image"))),
                        t.div({ className: "col-md-6" }, field("Music", modelField("models.music", "music"))),
                        t.div({ className: "col-md-6" }, field("Daily budget, US dollars", num("budget", { step: "0.5", min: "0" }),
                            () => `Spent today (UTC): $${data.spent.toFixed(3)}.`)),
                        t.div({ className: "col-lg-12" }, estimateView),
                    ),
                ),
                () => data.message
                    ? t.div({ className: "col-lg-12" }, t.div({ className: `alert ${data.message.kind}` }, t.div({ className: "content" }, data.message.text)))
                    : null,
                t.div({ className: "col-lg-12" }, t.hr()),
                t.div({ className: "col-lg-12" },
                    t.div({ className: "flex" },
                        t.button({
                            type: "button", className: "btn transparent secondary", disabled: () => data.saving,
                            onclick: () => confirm("Put back the default tuning for everything on this page?") && save(true),
                        }, t.span({ className: "txt" }, "Put back the defaults")),
                        t.button({ type: "button", className: "btn transparent secondary", disabled: () => data.saving, onclick: exportJSON },
                            t.i({ className: "ri-download-2-line", ariaHidden: true }), t.span({ className: "txt" }, "Export")),
                        t.button({ type: "button", className: "btn transparent secondary", disabled: () => data.saving, onclick: importJSON },
                            t.i({ className: "ri-upload-2-line", ariaHidden: true }), t.span({ className: "txt" }, "Import")),
                        t.div({ className: "m-r-auto" }),
                        t.button({
                            type: "button", className: () => `btn expanded-lg ${data.saving ? "loading" : ""}`, disabled: () => data.saving,
                            onclick: () => save(false),
                        }, t.span({ className: "txt" }, "Save changes")),
                    ),
                ),
            );
        }

        load();
        app.pb.files.getToken({ requestKey: null }).then((x) => { data.fileToken = x; }).catch(() => {});

        return t.div(
            { className: "page page-storytime-settings" },
            sidebar(),
            t.div(
                { className: "page-content full-height" },
                t.header({ className: "page-header" },
                    t.nav({ className: "breadcrumbs" },
                        t.div({ className: "breadcrumb-item" }, "Story Time"),
                        t.div({ className: "breadcrumb-item" }, section.title))),
                t.div({ className: "wrapper m-b-base" }, () => {
                    data.version; // redraw when the values are replaced
                    if (data.loading) return t.div({ className: "block txt-center" }, t.span({ className: "loader lg" }));
                    if (!v) return null;
                    return form();
                }),
                t.footer({ className: "page-footer" }, app.components.credits()),
            ),
        );
    }
})();
