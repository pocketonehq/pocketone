// Point-in-time restore's own settings on Settings → Backups (PocketOne ext/pitr): where the history is kept.
// Restoring is the restore/v1 contract's screen ("Restore to a moment"), which works for any provider.
// Served at /_/extensions/pitr/main.js. Plain DOM plus the dashboard's app.pb client.

(() => {
    const el = (tag, props = {}, ...children) => {
        const node = document.createElement(tag);
        for (const [k, v] of Object.entries(props)) {
            if (k === "className") node.className = v;
            else if (k.startsWith("on")) node.addEventListener(k.slice(2), v);
            else node.setAttribute(k, v);
        }
        for (const c of children.flat()) {
            if (c == null || c === false) continue;
            node.append(c instanceof Node ? c : document.createTextNode(String(c)));
        }
        return node;
    };
    const size = (b) => (b < 1 << 20 ? `${Math.max(1, Math.round(b / 1024))} KiB` : `${(b / (1 << 20)).toFixed(1)} MiB`);

    // pitr's facts come from restore/v1 (details), but only when pitr is the provider
    async function status() {
        const p = await app.pb.send("/api/_c/restore/v1", { requestKey: null });
        if (p.provider !== "pitr") throw new Error("pitr is off for this app");
        return { ...(p.details || {}), window: p.window };
    }

    // Where the history goes: the app's own S3 bucket (this form), else the server's storage, else this server's disk.
    async function storageForm(st, section) {
        const cfg = (await app.pb.send("/api/_c/settings/v1/get", { query: { extension: "pitr" }, requestKey: null })).values;
        const s3 = cfg.s3;
        const field = (label, name, value, type = "text", placeholder = "") =>
            el("label", { className: "flex gap-10 m-b-sm" },
                el("span", { style: "min-width:9rem" }, label),
                el("input", { type, name, value: value ?? "", placeholder, style: "flex:1", autocomplete: "off" }));
        const enabled = el("input", { type: "checkbox", name: "enabled" });
        enabled.checked = !!s3.enabled;
        const pathStyle = el("input", { type: "checkbox", name: "forcePathStyle" });
        pathStyle.checked = !!s3.forcePathStyle;
        const msg = el("div", { className: "m-t-sm" });
        const form = el("form", { className: "m-t-sm" },
            el("label", { className: "flex gap-10 m-b-sm" }, enabled, el("span", {}, "Store the history in my own S3 bucket")),
            field("Endpoint", "endpoint", s3.endpoint, "text", "https://s3.example.com"),
            field("Bucket", "bucket", s3.bucket),
            field("Region", "region", s3.region),
            field("Access key", "accessKey", s3.accessKey),
            field("Secret", "secret", "", "password", s3.secretSet ? "unchanged (leave empty)" : ""),
            field("Key prefix", "prefix", s3.prefix, "text", "litestream"),
            el("label", { className: "flex gap-10 m-b-sm" }, pathStyle, el("span", {}, "Force path-style addressing (e.g. MinIO)")),
            el("p", { className: "txt-hint" },
                "Changing where the history goes starts it again at the new place (a fresh snapshot). History already stored at the old place stays there and isn't used for restores."),
        );
        const body = () => {
            const v = (n) => form.querySelector(`[name="${n}"]`).value.trim();
            return { s3: { enabled: enabled.checked, endpoint: v("endpoint"), bucket: v("bucket"), region: v("region"),
                accessKey: v("accessKey"), secret: v("secret"), prefix: v("prefix"), forcePathStyle: pathStyle.checked } };
        };
        const show = (cls, text) => msg.replaceChildren(el("div", { className: `alert ${cls}` }, text));
        const test = el("button", { type: "button", className: "btn sm secondary" }, "Test connection");
        test.addEventListener("click", async () => {
            show("info", "Testing…");
            try {
                const r = await app.pb.send("/api/_c/settings/v1/test", { method: "POST", body: { extension: "pitr", values: body() }, requestKey: null });
                if (!r.ok) throw new Error(r.message);
                show("success", "The bucket accepted a test write.");
            } catch (err) {
                show("danger", err?.response?.message || err?.message || String(err));
            }
        });
        const save = el("button", { type: "submit", className: "btn sm" }, "Save");
        form.append(el("div", { className: "flex gap-10" }, save, test), msg);
        form.addEventListener("submit", async (e) => {
            e.preventDefault();
            try {
                await app.pb.send("/api/_c/settings/v1/save", { method: "POST", body: { extension: "pitr", values: body() }, requestKey: null });
                render(section);
            } catch (err) {
                show("danger", err?.response?.message || err?.message || String(err));
            }
        });
        const where = { s3: "your S3 bucket", storage: "the server's storage", local: "this server's disk" }[st.destination] || st.destination;
        return el("details", { className: "m-t-base" },
            el("summary", { className: "txt-bold" }, `Storage: ${where}${st.label ? ` (${st.label})` : ""}`),
            form);
    }

    async function render(section) {
        let st;
        try {
            st = await status();
        } catch (_) {
            section.remove(); // pitr isn't on for this app
            return;
        }
        const where = { s3: "your S3 bucket", storage: "the server's storage", local: "this server's disk" }[st.destination] || st.destination;
        section.replaceChildren(
            el("div", { className: "txt-lg m-b-sm" }, "Point-in-time restore"),
            el("p", { className: "txt-hint" },
                st.window ? `History: ${size(st.size)}, stored on ${where}, kept for ${st.retention}. ` : "No history yet. ",
                "Covers the database (records, collections, settings), not uploaded files."),
            await storageForm(st, section),
        );
    }

    document.addEventListener("mount:pageBackupsSettings", (e) => {
        const grid = e.detail.querySelector(".page-content .wrapper .grid") || e.detail;
        const section = el("div", { className: "col-lg-12 pitr-section" });
        grid.prepend(section);
        render(section);
    });
})();
