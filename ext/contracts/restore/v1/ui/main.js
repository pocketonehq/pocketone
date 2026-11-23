// "Restore to a moment" on Settings → Backups, for the restore/v1 contract (PocketOne ext/contracts/restore/v1).
// It works the same whoever provides the contract: pitr (any second in its window) or backups (the latest backup at
// or before the moment). Served at /_/extensions/restore/main.js. Plain DOM plus the dashboard's app.pb client.
(() => {
    const api = "/api/_c/restore/v1";
    const el = (tag, props = {}, ...children) => {
        const node = document.createElement(tag);
        for (const [k, v] of Object.entries(props)) {
            if (v == null || v === false) continue;
            if (k === "className") node.className = v;
            else if (k.startsWith("on")) node.addEventListener(k.slice(2), v);
            else node.setAttribute(k, v === true ? "" : v);
        }
        for (const c of children.flat()) {
            if (c == null || c === false) continue;
            node.append(c instanceof Node ? c : document.createTextNode(String(c)));
        }
        return node;
    };
    const fmt = (iso) => (iso ? new Date(iso).toLocaleString() : "");
    const localInput = (d) => {
        const p = (n) => String(n).padStart(2, "0");
        return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
    };

    // the snapshot a moment restores from: the newest at or before it
    const pick = (points, at) => (points.snapshots || []).find((s) => new Date(s.at) <= at) || null;

    async function waitUntilBack() {
        // the app restarts to apply the restore; poll until it answers again and the restore is done
        for (let i = 0; i < 180; i++) {
            await new Promise((r) => setTimeout(r, 1000));
            try {
                const p = await app.pb.send(api, { requestKey: null });
                if (!p.restoring) return;
            } catch (_) {}
        }
    }

    async function restore(at, section, points) {
        const when = new Date(at).toLocaleString();
        let what = `the app's data as it was at ${when}`;
        if (points.kind === "snapshots") {
            const s = pick(points, new Date(at));
            if (!s) return app.toasts.error(`There's no backup at or before ${when}.`);
            what = `the backup ${s.name} (made ${fmt(s.at)})`;
        }
        if (!confirm(`Restore ${what}?\n\nThe app restarts. Changes made after that point are replaced.`)) return;
        section.replaceChildren(el("div", { className: "alert info" }, `Restoring ${what}… the app restarts; this page reloads when it's back.`));
        try {
            await app.pb.send(api + "/restore", { method: "POST", body: { at: new Date(at).toISOString() }, requestKey: null });
        } catch (err) {
            app.checkApiError(err);
            render(section);
            return;
        }
        await waitUntilBack();
        window.location.reload();
    }

    async function render(section) {
        let p;
        try {
            p = await app.pb.send(api, { requestKey: null });
        } catch (_) {
            section.remove(); // nothing provides restore/v1 here, or not a superuser
            return;
        }
        const children = [el("div", { className: "txt-lg m-b-sm" }, "Restore to a moment")];
        const continuous = p.kind === "continuous";

        if (continuous && !p.window) {
            children.push(el("p", { className: "txt-hint" }, "History starts with the first change after point-in-time restore was turned on."));
            section.replaceChildren(...children);
            return;
        }
        if (!continuous && !(p.snapshots || []).length) {
            children.push(el("p", { className: "txt-hint" }, "No backups yet. Create one below (or turn on automatic backups), and it can be restored here."));
            section.replaceChildren(...children);
            return;
        }
        children.push(el("p", { className: "txt-hint" }, continuous
            ? `Any moment from ${fmt(p.window.earliest)} to ${fmt(p.window.latest)} (point-in-time restore).`
            : `Restores the latest backup made at or before the moment you pick: ${p.snapshots.length} backup${p.snapshots.length === 1 ? "" : "s"}, the newest from ${fmt(p.snapshots[0].at)}. Turn on point-in-time restore to restore any moment instead.`));

        if (p.last) {
            children.push(el("div", { className: "alert info m-b-sm" },
                `Last restore: to ${fmt(p.last.point)}, on ${fmt(p.last.done)}. `,
                continuous && el("button", {
                    type: "button", className: "btn sm secondary",
                    onclick: () => restore(new Date(new Date(p.last.done).getTime() - 1000).toISOString(), section, p),
                }, "Undo this restore")));
        }

        const hint = el("span", { className: "txt-hint" });
        const input = el("input", {
            type: "datetime-local", step: "1",
            min: continuous ? localInput(new Date(p.window.earliest)) : null,
            max: localInput(new Date()),
            value: localInput(new Date(Date.now() - 5 * 60 * 1000)),
        });
        const update = () => {
            if (continuous || !input.value) return hint.replaceChildren();
            const s = pick(p, new Date(input.value));
            hint.textContent = s ? `uses ${s.name} (${fmt(s.at)})` : "no backup that early";
        };
        input.addEventListener("input", update);
        update();
        children.push(el("form", {
            className: "flex gap-10",
            onsubmit: (e) => { e.preventDefault(); if (input.value) restore(input.value, section, p); },
        },
            el("label", { className: "txt-bold" }, "Restore to"), input,
            el("button", { type: "submit", className: "btn warning", disabled: p.restoring }, "Restore"),
            hint));
        section.replaceChildren(...children);
    }

    document.addEventListener("mount:pageBackupsSettings", (e) => {
        const grid = e.detail.querySelector(".page-content .wrapper .grid") || e.detail;
        const section = el("div", { className: "col-lg-12 restore-section" });
        grid.prepend(section);
        render(section);
    });
})();
