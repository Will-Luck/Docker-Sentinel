/* ============================================================
   Docker-Sentinel Dashboard v3 — Client-side JavaScript
   ES5-compatible (no let/const/arrow functions)
   ============================================================ */

/* ------------------------------------------------------------
   1. Theme System
   ------------------------------------------------------------ */

function initTheme() {
    var saved = localStorage.getItem("sentinel-theme") || "dark";
    applyTheme(saved);
}

function cycleTheme() {
    var current = localStorage.getItem("sentinel-theme") || "dark";
    var next;
    if (current === "dark") {
        next = "light";
    } else if (current === "light") {
        next = "auto";
    } else {
        next = "dark";
    }
    applyTheme(next);
    localStorage.setItem("sentinel-theme", next);
}

function applyTheme(theme) {
    var root = document.documentElement;
    var icon = document.getElementById("theme-icon");
    var label = document.getElementById("theme-label");

    if (theme === "dark") {
        root.style.colorScheme = "dark";
        if (icon) icon.textContent = "\u263E";
        if (label) label.textContent = "Dark";
    } else if (theme === "light") {
        root.style.colorScheme = "light";
        if (icon) icon.textContent = "\u263C";
        if (label) label.textContent = "Light";
    } else {
        root.style.colorScheme = "dark light";
        if (icon) icon.textContent = "\u2699";
        if (label) label.textContent = "Auto";
    }

    localStorage.setItem("sentinel-theme", theme);
}

/* ------------------------------------------------------------
   2. Sidebar
   ------------------------------------------------------------ */

function initSidebar() {
    var collapsed = localStorage.getItem("sentinel-sidebar") === "collapsed";
    if (collapsed) {
        var layout = document.querySelector(".layout");
        if (layout) layout.classList.add("sidebar-collapsed");
    }
}

function toggleSidebar() {
    var layout = document.querySelector(".layout");
    if (!layout) return;
    layout.classList.toggle("sidebar-collapsed");
    var isCollapsed = layout.classList.contains("sidebar-collapsed");
    localStorage.setItem("sentinel-sidebar", isCollapsed ? "collapsed" : "expanded");
}

/* ------------------------------------------------------------
   3. Toast System
   ------------------------------------------------------------ */

function showToast(message, type) {
    var container = document.getElementById("toast-container");
    if (!container) {
        container = document.createElement("div");
        container.id = "toast-container";
        container.className = "toast-container";
        document.body.appendChild(container);
    }

    var toast = document.createElement("div");
    toast.className = "toast toast-" + type;
    toast.textContent = message;
    container.appendChild(toast);

    setTimeout(function () {
        toast.style.animation = "fadeOut 0.3s ease-out forwards";
        setTimeout(function () {
            if (toast.parentNode) toast.parentNode.removeChild(toast);
        }, 300);
    }, 4000);
}

/* ------------------------------------------------------------
   4. HTML Escape Helper
   ------------------------------------------------------------ */

function escapeHTML(str) {
    var div = document.createElement("div");
    div.appendChild(document.createTextNode(str));
    return div.innerHTML;
}

/* ------------------------------------------------------------
   5. API Actions
   ------------------------------------------------------------ */

function apiPost(url, body, successMsg, errorMsg) {
    var opts = { method: "POST" };
    if (body) {
        opts.headers = { "Content-Type": "application/json" };
        opts.body = JSON.stringify(body);
    }
    fetch(url, opts)
        .then(function (resp) {
            return resp.json().then(function (data) {
                return { ok: resp.ok, data: data };
            });
        })
        .then(function (result) {
            if (result.ok) {
                showToast(result.data.message || successMsg, "success");
            } else {
                showToast(result.data.error || errorMsg, "error");
            }
        })
        .catch(function () {
            showToast("Network error — " + errorMsg.toLowerCase(), "error");
        });
}

function approveUpdate(name) {
    apiPost(
        "/api/approve/" + encodeURIComponent(name),
        null,
        "Approved update for " + name,
        "Failed to approve"
    );
}

function rejectUpdate(name) {
    apiPost(
        "/api/reject/" + encodeURIComponent(name),
        null,
        "Rejected update for " + name,
        "Failed to reject"
    );
}

function triggerUpdate(name) {
    apiPost(
        "/api/update/" + encodeURIComponent(name),
        null,
        "Update started for " + name,
        "Failed to trigger update"
    );
}

function triggerRollback(name) {
    apiPost(
        "/api/containers/" + encodeURIComponent(name) + "/rollback",
        null,
        "Rollback started for " + name,
        "Failed to trigger rollback"
    );
}

function changePolicy(name, newPolicy) {
    apiPost(
        "/api/containers/" + encodeURIComponent(name) + "/policy",
        { policy: newPolicy },
        "Policy changed to " + newPolicy + " for " + name,
        "Failed to change policy"
    );
}

function updateToVersion(name) {
    var sel = document.getElementById("version-select");
    if (!sel) return;
    var version = sel.value;
    showToast("Version pinning to " + version + " is not yet implemented", "info");
}

function applyBulkPolicy() {
    var names = [];
    var keys = Object.keys(selectedContainers);
    for (var i = 0; i < keys.length; i++) {
        if (selectedContainers[keys[i]]) names.push(keys[i]);
    }
    if (names.length === 0) return;

    var policyEl = document.getElementById("bulk-policy");
    var policy = policyEl ? policyEl.value : "manual";

    fetch("/api/bulk/policy", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ containers: names, policy: policy })
    })
        .then(function (resp) {
            return resp.json().then(function (data) {
                return { ok: resp.ok, data: data };
            });
        })
        .then(function (result) {
            if (result.ok) {
                showToast(result.data.message || "Bulk policy change started", "success");
                clearSelection();
            } else {
                showToast(result.data.error || "Failed to apply bulk policy", "error");
            }
        })
        .catch(function () {
            showToast("Network error — could not apply bulk policy", "error");
        });
}

/* ------------------------------------------------------------
   6. Row Click Delegation
   ------------------------------------------------------------ */

function onRowClick(e, name) {
    var tag = e.target.tagName;
    if (tag === "A" || tag === "BUTTON" || tag === "SELECT" || tag === "INPUT" || tag === "OPTION") {
        return;
    }
    toggleAccordion(name);
}

/* ------------------------------------------------------------
   7. Stack Toggle
   ------------------------------------------------------------ */

function toggleStack(headerRow) {
    var group = headerRow.closest(".stack-group");
    if (!group) return;
    group.classList.toggle("stack-collapsed");
}

function expandAllStacks() {
    var groups = document.querySelectorAll(".stack-group");
    for (var i = 0; i < groups.length; i++) {
        groups[i].classList.remove("stack-collapsed");
    }
}

function collapseAllStacks() {
    var panels = document.querySelectorAll(".accordion-panel");
    for (var i = 0; i < panels.length; i++) {
        panels[i].style.display = "none";
    }
    var groups = document.querySelectorAll(".stack-group");
    for (var i = 0; i < groups.length; i++) {
        groups[i].classList.add("stack-collapsed");
    }
}

/* ------------------------------------------------------------
   8. Accordion (lazy-load from API)
   ------------------------------------------------------------ */

var accordionCache = {};

function toggleAccordion(name) {
    var panel = document.getElementById("accordion-" + name);
    if (!panel) return;

    var isOpen = panel.style.display !== "none";
    if (isOpen) {
        panel.style.display = "none";
        return;
    }

    panel.style.display = "";

    // If the panel already has server-rendered content, skip fetching.
    var contentEl = panel.querySelector(".accordion-content");
    if (contentEl && contentEl.querySelector(".accordion-grid")) return;

    // Use cache if available.
    if (accordionCache[name]) {
        renderAccordionContent(name, accordionCache[name]);
        return;
    }

    // Show loading state.
    if (contentEl) contentEl.textContent = "Loading\u2026";

    // Lazy-load from API (parallel requests).
    var enc = encodeURIComponent(name);
    Promise.all([
        fetch("/api/containers/" + enc).then(function (r) { return r.json(); }),
        fetch("/api/containers/" + enc + "/versions").then(function (r) { return r.json(); })
    ]).then(function (results) {
        var data = { detail: results[0], versions: results[1] };
        accordionCache[name] = data;
        renderAccordionContent(name, data);
    }).catch(function () {
        if (contentEl) contentEl.textContent = "Failed to load data";
    });
}

function renderAccordionContent(name, data) {
    var panel = document.getElementById("accordion-" + name);
    if (!panel) return;
    var contentEl = panel.querySelector(".accordion-content");
    if (!contentEl) return;

    var d = data.detail;
    var versions = data.versions || [];

    // Clear existing content safely.
    while (contentEl.firstChild) contentEl.removeChild(contentEl.firstChild);

    var grid = document.createElement("div");
    grid.className = "accordion-grid";

    // --- Info section ---
    var infoSection = document.createElement("div");
    infoSection.className = "accordion-section";

    function addField(parent, labelText, valueText, extraClass) {
        var lbl = document.createElement("div");
        lbl.className = "accordion-label";
        lbl.textContent = labelText;
        parent.appendChild(lbl);
        var val = document.createElement("div");
        val.className = "accordion-value" + (extraClass ? " " + extraClass : "");
        val.textContent = valueText;
        parent.appendChild(val);
    }

    addField(infoSection, "Image", d.image || "", "mono");
    addField(infoSection, "State", d.state || "");
    addField(infoSection, "Policy", d.policy || "");

    if (d.maintenance) {
        var mLabel = document.createElement("div");
        mLabel.className = "accordion-label";
        mLabel.textContent = "Maintenance";
        infoSection.appendChild(mLabel);
        var mVal = document.createElement("div");
        mVal.className = "accordion-value";
        var mBadge = document.createElement("span");
        mBadge.className = "badge badge-warning";
        mBadge.textContent = "In progress";
        mVal.appendChild(mBadge);
        infoSection.appendChild(mVal);
    }

    grid.appendChild(infoSection);

    // --- Versions section ---
    var verSection = document.createElement("div");
    verSection.className = "accordion-section";
    var verLabel = document.createElement("div");
    verLabel.className = "accordion-label";
    verLabel.textContent = "Available Versions";
    verSection.appendChild(verLabel);

    if (versions.length > 0) {
        var verWrap = document.createElement("div");
        verWrap.className = "accordion-versions";
        var limit = Math.min(versions.length, 8);
        for (var i = 0; i < limit; i++) {
            var badge = document.createElement("span");
            badge.className = "version-badge";
            badge.textContent = versions[i];
            verWrap.appendChild(badge);
        }
        if (versions.length > 8) {
            var more = document.createElement("span");
            more.className = "text-muted";
            more.textContent = "+" + (versions.length - 8) + " more";
            verWrap.appendChild(more);
        }
        verSection.appendChild(verWrap);
    } else {
        var noVer = document.createElement("div");
        noVer.className = "text-muted";
        noVer.textContent = "No newer versions found";
        verSection.appendChild(noVer);
    }

    grid.appendChild(verSection);

    // --- Actions section ---
    var actSection = document.createElement("div");
    actSection.className = "accordion-section accordion-actions";

    if (d.snapshots && d.snapshots.length > 0) {
        var rbBtn = document.createElement("button");
        rbBtn.className = "btn btn-error";
        rbBtn.textContent = "Rollback";
        rbBtn.addEventListener("click", function () { triggerRollback(name); });
        actSection.appendChild(rbBtn);
    }

    var detailLink = document.createElement("a");
    detailLink.href = "/container/" + encodeURIComponent(name);
    detailLink.className = "btn btn-info";
    detailLink.textContent = "Full Details";
    actSection.appendChild(detailLink);

    grid.appendChild(actSection);
    contentEl.appendChild(grid);
}

/* ------------------------------------------------------------
   9. Multi-select
   ------------------------------------------------------------ */

var selectedContainers = {};

function updateSelectionUI() {
    var names = Object.keys(selectedContainers);
    var count = 0;
    for (var i = 0; i < names.length; i++) {
        if (selectedContainers[names[i]]) count++;
    }

    var bar = document.getElementById("bulk-bar");
    var countEl = document.getElementById("bulk-count");
    if (count > 0) {
        if (bar) bar.style.display = "";
        if (countEl) countEl.textContent = count + " selected";
        document.body.style.paddingBottom = "70px";
    } else {
        if (bar) bar.style.display = "none";
        document.body.style.paddingBottom = "";
    }
}

function clearSelection() {
    selectedContainers = {};
    var checkboxes = document.querySelectorAll(".row-select");
    for (var i = 0; i < checkboxes.length; i++) {
        checkboxes[i].checked = false;
    }
    var selectAll = document.getElementById("select-all");
    if (selectAll) selectAll.checked = false;
    updateSelectionUI();
}

/* ------------------------------------------------------------
   10. SSE Real-time Updates
   ------------------------------------------------------------ */

var sseReloadTimer = null;

function scheduleReload() {
    accordionCache = {};
    if (sseReloadTimer) clearTimeout(sseReloadTimer);
    sseReloadTimer = setTimeout(function () {
        window.location.reload();
    }, 800);
}

function setConnectionStatus(connected) {
    var dot = document.getElementById("sse-indicator");
    var label = document.getElementById("sse-label");
    if (!dot) return;

    if (connected) {
        dot.className = "status-dot connected";
        dot.title = "Live";
        if (label) label.textContent = "Live";
    } else {
        dot.className = "status-dot disconnected";
        dot.title = "Reconnecting\u2026";
        if (label) label.textContent = "Reconnecting\u2026";
    }
}

function initSSE() {
    if (typeof EventSource === "undefined") return;

    var es = new EventSource("/api/events");

    es.addEventListener("connected", function () {
        setConnectionStatus(true);
    });

    es.addEventListener("container_update", function (e) {
        try {
            var data = JSON.parse(e.data);
            showToast(data.message || ("Update: " + data.container_name), "info");
        } catch (_) {}
        scheduleReload();
    });

    es.addEventListener("container_state", function () {
        scheduleReload();
    });

    es.addEventListener("queue_change", function (e) {
        try {
            var data = JSON.parse(e.data);
            showToast(data.message || "Queue updated", "info");
        } catch (_) {}
        scheduleReload();
    });

    es.addEventListener("scan_complete", function () {
        scheduleReload();
    });

    es.addEventListener("policy_change", function (e) {
        try {
            var data = JSON.parse(e.data);
            showToast(data.message || ("Policy changed: " + data.container_name), "info");
        } catch (_) {}
        scheduleReload();
    });

    es.onopen = function () {
        setConnectionStatus(true);
    };

    es.onerror = function () {
        setConnectionStatus(false);
    };
}

/* ------------------------------------------------------------
   11. Initialisation
   ------------------------------------------------------------ */

document.addEventListener("DOMContentLoaded", function () {
    initTheme();
    initSidebar();
    initSSE();

    // Multi-select: checkbox delegation on container table.
    var table = document.getElementById("container-table");
    if (table) {
        table.addEventListener("change", function (e) {
            var target = e.target;

            // Select-all checkbox
            if (target.id === "select-all") {
                var checkboxes = table.querySelectorAll(".row-select");
                for (var i = 0; i < checkboxes.length; i++) {
                    var row = checkboxes[i].closest(".container-row");
                    if (row && row.offsetParent !== null) {
                        checkboxes[i].checked = target.checked;
                        selectedContainers[checkboxes[i].value] = target.checked;
                    }
                }
                updateSelectionUI();
                return;
            }

            // Individual row checkbox
            if (target.classList.contains("row-select")) {
                selectedContainers[target.value] = target.checked;
                updateSelectionUI();
            }
        });
    }
});
