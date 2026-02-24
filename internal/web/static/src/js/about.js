/* ============================================================
   About tab & Release note sources
   ============================================================ */

import { showToast } from "./utils.js";
import { getCSRFToken } from "./csrf.js";

function loadFooterVersion() {
    var el = document.getElementById("footer-version");
    if (!el) return;
    fetch("/api/about")
        .then(function(r) { return r.json(); })
        .then(function(data) {
            el.textContent = "Docker-Sentinel " + (data.version || "dev");
        })
        .catch(function() { /* ignore — falls back to defaults */ });
}

function loadAboutInfo() {
    var container = document.getElementById("about-content");
    if (!container) return;

    fetch("/api/about")
        .then(function(r) { return r.json(); })
        .then(function(data) {
            var rows = document.createElement("div");
            rows.className = "settings-rows";

            appendAboutSection(rows, "Instance");
            appendAboutRow(rows, "Version", data.version || "dev");
            appendAboutRow(rows, "Go Version", data.go_version || "-");
            appendAboutRow(rows, "Data Directory", data.data_directory || "-");
            appendAboutRow(rows, "Uptime", data.uptime || "-");
            appendAboutRow(rows, "Started", data.started_at ? formatAboutTime(data.started_at) : "-");

            appendAboutSection(rows, "Runtime");
            appendAboutRow(rows, "Poll Interval", data.poll_interval || "-");
            appendAboutRow(rows, "Last Scan", data.last_scan ? formatAboutTimeAgo(data.last_scan) : "Never");
            appendAboutRow(rows, "Containers Monitored", String(data.containers || 0));
            appendAboutRow(rows, "Updates Applied", String(data.updates_applied || 0));
            appendAboutRow(rows, "Snapshots Stored", String(data.snapshots || 0));

            appendAboutSection(rows, "Integrations");

            // Notification channels
            if (data.channels && data.channels.length > 0) {
                var chWrap = document.createElement("div");
                chWrap.className = "about-channels";
                for (var i = 0; i < data.channels.length; i++) {
                    var badge = document.createElement("span");
                    badge.className = "about-channel-badge";
                    badge.textContent = data.channels[i].name;
                    var typeSpan = document.createElement("span");
                    typeSpan.className = "about-channel-type";
                    typeSpan.textContent = data.channels[i].type;
                    badge.appendChild(typeSpan);
                    chWrap.appendChild(badge);
                }
                appendAboutRowEl(rows, "Notification Channels", chWrap);
            } else {
                appendAboutRow(rows, "Notification Channels", "None configured");
            }

            // Registry auth
            if (data.registries && data.registries.length > 0) {
                var regWrap = document.createElement("div");
                regWrap.className = "about-channels";
                for (var i = 0; i < data.registries.length; i++) {
                    var regBadge = document.createElement("span");
                    regBadge.className = "about-channel-badge";
                    regBadge.textContent = data.registries[i];
                    regWrap.appendChild(regBadge);
                }
                appendAboutRowEl(rows, "Registry Auth", regWrap);
            } else {
                appendAboutRow(rows, "Registry Auth", "None configured");
            }

            // Beta banner
            var banner = document.createElement("div");
            banner.className = "about-banner";
            var bannerIcon = document.createElement("span");
            bannerIcon.className = "about-banner-icon";
            bannerIcon.textContent = "\u24D8";
            banner.appendChild(bannerIcon);
            var bannerText = document.createElement("span");
            bannerText.textContent = "This is BETA software. Features may be broken and/or unstable. Please report any issues on ";
            var bannerLink = document.createElement("a");
            bannerLink.href = "https://github.com/Will-Luck/Docker-Sentinel/issues";
            bannerLink.target = "_blank";
            bannerLink.rel = "noopener";
            bannerLink.textContent = "GitHub";
            bannerText.appendChild(bannerLink);
            bannerText.appendChild(document.createTextNode("!"));
            banner.appendChild(bannerText);

            // Links section
            appendAboutSection(rows, "Links");
            var linksWrap = document.createElement("div");
            linksWrap.className = "about-links";
            var links = [
                { icon: "\uD83D\uDCC1", label: "GitHub", url: "https://github.com/Will-Luck/Docker-Sentinel" },
                { icon: "\uD83D\uDC1B", label: "Report a Bug", url: "https://github.com/Will-Luck/Docker-Sentinel/issues/new?template=bug_report.md" },
                { icon: "\uD83D\uDCA1", label: "Feature Request", url: "https://github.com/Will-Luck/Docker-Sentinel/issues/new?template=feature_request.md" },
                { icon: "\uD83D\uDCC4", label: "Releases", url: "https://github.com/Will-Luck/Docker-Sentinel/releases" }
            ];
            for (var li = 0; li < links.length; li++) {
                var a = document.createElement("a");
                a.className = "about-link";
                a.href = links[li].url;
                a.target = "_blank";
                a.rel = "noopener";
                var ico = document.createElement("span");
                ico.className = "about-link-icon";
                ico.textContent = links[li].icon;
                a.appendChild(ico);
                a.appendChild(document.createTextNode(links[li].label));
                linksWrap.appendChild(a);
            }
            var linksRow = document.createElement("div");
            linksRow.className = "setting-row";
            linksRow.appendChild(linksWrap);
            rows.appendChild(linksRow);

            container.textContent = "";
            container.appendChild(banner);
            container.appendChild(rows);
        })
        .catch(function() {
            container.textContent = "Failed to load info";
        });
}

function appendAboutSection(parent, title) {
    var div = document.createElement("div");
    div.className = "about-section-title";
    div.textContent = title;
    parent.appendChild(div);
}

function appendAboutRow(parent, label, value) {
    var row = document.createElement("div");
    row.className = "setting-row";
    var info = document.createElement("div");
    info.className = "setting-info";
    var lbl = document.createElement("div");
    lbl.className = "setting-label";
    lbl.textContent = label;
    info.appendChild(lbl);
    row.appendChild(info);
    var val = document.createElement("div");
    val.className = "about-value";
    val.textContent = value;
    row.appendChild(val);
    parent.appendChild(row);
}

function appendAboutRowEl(parent, label, valueEl) {
    var row = document.createElement("div");
    row.className = "setting-row";
    var info = document.createElement("div");
    info.className = "setting-info";
    var lbl = document.createElement("div");
    lbl.className = "setting-label";
    lbl.textContent = label;
    info.appendChild(lbl);
    row.appendChild(info);
    row.appendChild(valueEl);
    parent.appendChild(row);
}

function formatAboutTime(iso) {
    try {
        var d = new Date(iso);
        return d.toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" }) +
            " " + d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
    } catch(e) {
        return iso;
    }
}

function formatAboutTimeAgo(iso) {
    try {
        var d = new Date(iso);
        var now = new Date();
        var diff = now - d;
        var mins = Math.floor(diff / 60000);
        if (mins < 1) return "Just now";
        if (mins < 60) return mins + "m ago";
        var hours = Math.floor(mins / 60);
        if (hours < 24) return hours + "h " + (mins % 60) + "m ago";
        var days = Math.floor(hours / 24);
        return days + "d " + (hours % 24) + "h ago";
    } catch(e) {
        return iso;
    }
}

// ---------------------------------------------------------------------------
// Release note sources
// ---------------------------------------------------------------------------

var releaseSources = [];

function loadReleaseSources() {
    var container = document.getElementById("release-sources-list");
    if (!container) return;
    fetch("/api/release-sources")
        .then(function(r) { return r.json(); })
        .then(function(data) {
            releaseSources = Array.isArray(data) ? data : [];
            renderReleaseSources();
        })
        .catch(function() {});
}

function renderReleaseSources() {
    var container = document.getElementById("release-sources-list");
    if (!container) return;
    container.textContent = "";

    if (releaseSources.length === 0) {
        var empty = document.createElement("div");
        empty.className = "empty-state";
        empty.style.padding = "var(--sp-6) var(--sp-4)";
        var h3 = document.createElement("h3");
        h3.textContent = "No custom sources configured";
        var p = document.createElement("p");
        p.textContent = "Add mappings to fetch release notes for images not covered by built-in rules.";
        empty.appendChild(h3);
        empty.appendChild(p);
        container.appendChild(empty);
        return;
    }

    releaseSources.forEach(function(src, index) {
        var card = document.createElement("div");
        card.className = "channel-card";

        var header = document.createElement("div");
        header.className = "channel-card-header";
        var badge = document.createElement("span");
        badge.className = "channel-type-badge";
        badge.textContent = src.image_pattern || "source";
        header.appendChild(badge);

        var actions = document.createElement("div");
        actions.className = "channel-actions";
        var delBtn = document.createElement("button");
        delBtn.className = "btn btn-sm btn-error";
        delBtn.textContent = "Remove";
        (function(i) {
            delBtn.addEventListener("click", function() { deleteReleaseSource(i); });
        })(index);
        actions.appendChild(delBtn);
        header.appendChild(actions);
        card.appendChild(header);

        var fields = document.createElement("div");
        fields.className = "channel-fields";
        [
            { label: "Image Pattern", field: "image_pattern", value: src.image_pattern },
            { label: "GitHub Repo", field: "github_repo", value: src.github_repo }
        ].forEach(function(def) {
            var row = document.createElement("div");
            row.className = "channel-field";
            var lbl = document.createElement("span");
            lbl.className = "channel-field-label";
            lbl.textContent = def.label;
            row.appendChild(lbl);
            var inp = document.createElement("input");
            inp.type = "text";
            inp.className = "channel-field-input";
            inp.value = def.value || "";
            inp.setAttribute("data-index", index);
            inp.setAttribute("data-field", def.field);
            row.appendChild(inp);
            fields.appendChild(row);
        });
        card.appendChild(fields);
        container.appendChild(card);
    });
}

function addReleaseSource() {
    releaseSources.push({ image_pattern: "", github_repo: "" });
    renderReleaseSources();
}

function deleteReleaseSource(index) {
    releaseSources.splice(index, 1);
    renderReleaseSources();
}

function collectReleaseSourcesFromDOM() {
    var inputs = document.querySelectorAll("#release-sources-list input[data-field]");
    var map = {};
    inputs.forEach(function(inp) {
        var i = inp.getAttribute("data-index");
        var f = inp.getAttribute("data-field");
        if (!map[i]) map[i] = {};
        map[i][f] = inp.value.trim();
    });
    return Object.keys(map).sort(function(a,b){return a-b;}).map(function(k){ return map[k]; });
}

function saveReleaseSources(event) {
    if (event) event.preventDefault();
    var sources = collectReleaseSourcesFromDOM();
    fetch("/api/release-sources", {
        method: "PUT",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": getCSRFToken() },
        body: JSON.stringify(sources)
    }).then(function(r) {
        if (r.ok) {
            releaseSources = sources;
            showToast("Release sources saved", "success");
        } else {
            r.json().then(function(d) { showToast("Save failed: " + (d.error || r.status), "error"); });
        }
    }).catch(function() { showToast("Save failed", "error"); });
}


export {
    loadFooterVersion,
    loadAboutInfo,
    loadReleaseSources,
    addReleaseSource,
    saveReleaseSources
};
