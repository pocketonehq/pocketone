// Settings → Bedtime Story in the PocketBase dashboard: tune the narrator's voice, the prompts, the pictures, the music,
// the models and the daily budget, and try each with Sample before saving. A superuser UI extension, registered from
// pb_hooks/admin_plugins.pb.js (pb_admin_ext/bedtime); it calls the app's /admin/tuning and /admin/sample (superusers only), and the dashboard
// sends the signed-in superuser's token with each call.
(() => {
    const GROUP = "Bedtime Story";
    app.store.settingsNavGroups[GROUP] = [
        ...(app.store.settingsNavGroups[GROUP] || []),
        { href: "#/settings/bedtime", icon: "ri-moon-clear-line", label: "Tuning" },
    ];

    const send = (path, body) => app.pb.send(path, body ? { method: "POST", body, requestKey: null } : { requestKey: null });
    const errText = (err) => err?.response?.message || err?.message || String(err);

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

    function field(label, input, hint) {
        return t.div(null, t.div({ className: "field" }, t.label({}, label), input), hint ? t.div({ className: "field-help" }, hint) : null);
    }

    app.routes.superuserOnly("#/settings/bedtime", () => {
        app.store.title = "Bedtime Story";
        const data = store({
            loading: true,
            saving: false,
            message: null, // { kind, text }
            voices: [],
            voiceChoice: "", // the voice picked on the page, for its help text
            clips: [], // recordings a clone can imitate: { id, name, passage, created, file }
            clipChoice: "", // the recording picked on the page
            fileToken: "", // lets the superuser's browser play a recording (superusers only)
            check: null, // the last check of a recording: { id, busy } or { id, heard, match, missing, cutOff, model } or { id, error }
            rec: { state: "idle" }, // the recorder: idle, recording, saving, saved (seconds, name); error
            spent: 0,
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
        const area = (path, rows = 4) => t.textarea({ rows, value: get(path) ?? "", onchange: (e) => put(path, e.target.value),
            style: "font-family: var(--monospaceFontFamily, monospace); font-size: 12px; line-height: 1.5;" });

        async function load() {
            try {
                const out = await send("/admin/tuning");
                v = out.values;
                data.voices = out.voices || [];
                data.clips = out.clips || [];
                data.spent = out.spentToday || 0;
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
                v = out.values;
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
                a.download = `bedtime-${out.exported.slice(0, 10)}.json`;
                a.click();
                setTimeout(() => URL.revokeObjectURL(a.href), 10000);
                data.message = { kind: "success", text: `Exported the saved tuning and ${out.voices.length} recording${out.voices.length === 1 ? "" : "s"}. Unsaved changes on this page aren't in it.` };
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
                if (!confirm(`Import ${file.name}? It replaces the tuning on this page (saved) and adds its recordings.`)) return;
                data.saving = true;
                data.message = null;
                try {
                    const out = await send("/admin/import", JSON.parse(await file.text()));
                    v = out.values;
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
                    if (s.idea) return t.p({ className: "m-b-0" }, s.idea);
                    if (s.story) {
                        return t.div({ className: "m-t-sm" },
                            t.p({ className: "m-b-xs" }, t.strong(null, s.story.title), " · ", s.story.topic),
                            ...s.story.sentences.map((x) => t.p({ className: "m-b-xs" }, x)),
                            t.p({ className: "txt-hint m-b-0" }, "Pictures: ", s.story.look));
                    }
                    return null;
                },
            );
        }

        function block(title, about, ...children) {
            return t.details({ className: "accordion", open: true },
                t.summary(null, t.span({ className: "txt" }, title)),
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
                const ext = blob.type.includes("mp4") ? "m4a" : blob.type.includes("ogg") ? "ogg" : blob.type.includes("wav") ? "wav" : "webm";
                const form = new FormData();
                form.append("name", takeName.trim() || "My voice");
                form.append("passage", PASSAGE);
                form.append("audio", new File([blob], `voice.${ext}`, { type: blob.type.split(";")[0] }));
                const saved = await app.pb.collection("voices").create(form, { requestKey: null });
                await refreshClips();
                pickClip(saved.id);
                data.rec = { state: "saved", seconds, name: saved.name };
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
                            t.button({ type: "button", className: () => `btn sm transparent secondary ${data.check?.busy ? "loading" : ""}`,
                                disabled: () => !!data.check?.busy, onclick: () => checkClip(clip.id) },
                                t.span({ className: "txt" }, "Check")),
                            t.button({ type: "button", className: "btn sm transparent danger", onclick: () => deleteClip(clip.id) },
                                t.span({ className: "txt" }, "Delete")))
                        : null),
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
                                `Saved "${r.name}" (${r.seconds} s) and picked it above: play it there. `,
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
                    "How ", t.a({ href: "/", target: "_blank" }, "Bedtime Story"), " makes its stories. Sample tries a piece with what's on this page, ",
                    "saved or not; new stories use what's saved.")),
                t.div({ className: "col-lg-12 accordions" },
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
                    block("Writing the story", "Placeholders: {minutes}, {minWords}, {maxWords}, {anyTopic}. Keep the field names it mentions (title, language, topic, look, sentences): the answer's shape is fixed.",
                        t.div({ className: "col-lg-12" }, field("Instructions", area("prompts.write", 16))),
                        t.div({ className: "col-lg-12" }, field("Topic for the sample",
                            t.input({ type: "text", placeholder: "Empty: it picks", value: extra.storyTopic, onchange: (e) => (extra.storyTopic = e.target.value) }))),
                        sampleRow("story", "Sample a 1-minute story", () => extra.storyTopic),
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
                    block("Inspire me", null,
                        t.div({ className: "col-lg-12" }, field("Instructions", area("prompts.inspire", 5))),
                        sampleRow("idea", "Sample an idea"),
                    ),
                    block("Models and budget", "Each as provider/model: a provider in Settings → LLM, then its model.",
                        t.div({ className: "col-md-6" }, field("Writing", text("models.text"))),
                        t.div({ className: "col-md-6" }, field("Inspire me", text("models.idea"))),
                        t.div({ className: "col-md-4" }, field("Voice", text("models.speech"))),
                        t.div({ className: "col-md-4" }, field("Voice, other languages", text("models.speechOther"))),
                        t.div({ className: "col-md-4" }, field("Word timing", text("models.align"))),
                        t.div({ className: "col-md-4" }, field("Pictures", text("models.image"))),
                        t.div({ className: "col-md-4" }, field("Music", text("models.music"))),
                        t.div({ className: "col-md-4" }, field("Daily budget, US dollars", num("budget", { step: "0.5", min: "0" }),
                            () => `Spent today (UTC): $${data.spent.toFixed(3)}.`)),
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
            { className: "page page-bedtime-settings" },
            sidebar(),
            t.div(
                { className: "page-content full-height" },
                t.header({ className: "page-header" },
                    t.nav({ className: "breadcrumbs" },
                        t.div({ className: "breadcrumb-item" }, "Settings"),
                        t.div({ className: "breadcrumb-item" }, "Bedtime Story"))),
                t.div({ className: "wrapper m-b-base" }, () => {
                    data.version; // redraw when the values are replaced
                    if (data.loading) return t.div({ className: "block txt-center" }, t.span({ className: "loader lg" }));
                    if (!v) return null;
                    return form();
                }),
                t.footer({ className: "page-footer" }, app.components.credits()),
            ),
        );
    });
})();
