/* ============================================================
   Docker-Sentinel Dashboard v3 — Client-side JavaScript
   ES5-compatible (no let/const/arrow functions)
   ============================================================ */

/* ------------------------------------------------------------
   1. Theme System
   ------------------------------------------------------------ */

function initTheme() {
    var saved = localStorage.getItem("sentinel-theme") || "auto";
    applyTheme(saved);
}

function applyTheme(theme) {
    var root = document.documentElement;

    if (theme === "dark") {
        root.style.colorScheme = "dark";
    } else if (theme === "light") {
        root.style.colorScheme = "light";
    } else {
        root.style.colorScheme = "dark light";
    }

    localStorage.setItem("sentinel-theme", theme);
}

/* ------------------------------------------------------------
   2. Settings Page
   ------------------------------------------------------------ */

function initSettingsPage() {
    var themeSelect = document.getElementById("theme-select");
    var stackSelect = document.getElementById("stack-default");
    if (!themeSelect) return;

    themeSelect.value = localStorage.getItem("sentinel-theme") || "auto";
    stackSelect.value = localStorage.getItem("sentinel-stacks") || "collapsed";

    themeSelect.addEventListener("change", function() {
        applyTheme(themeSelect.value);
        localStorage.setItem("sentinel-theme", themeSelect.value);
        showToast("Theme updated", "success");
    });
    stackSelect.addEventListener("change", function() {
        localStorage.setItem("sentinel-stacks", stackSelect.value);
        showToast("Stack default updated", "success");
    });

    var pollSelect = document.getElementById("poll-interval");
    if (pollSelect) {
        // Fetch current interval from server settings.
        fetch("/api/settings")
            .then(function(r) { return r.json(); })
            .then(function(settings) {
                var current = settings["SENTINEL_POLL_INTERVAL"] || "6h0m0s";
                // Normalise Go duration to match option values.
                var normalised = current
                    .replace("0m0s", "").replace("0s", "")
                    .replace(/^0h/, "");
                var matched = false;
                for (var i = 0; i < pollSelect.options.length; i++) {
                    if (pollSelect.options[i].value === normalised) {
                        pollSelect.selectedIndex = i;
                        matched = true;
                        break;
                    }
                }
                if (!matched && normalised !== "") {
                    // Set to custom and show the custom field.
                    pollSelect.value = "custom";
                    var customInput = document.getElementById("poll-custom");
                    var customBtn = document.getElementById("poll-custom-btn");
                    if (customInput) { customInput.style.display = ""; customInput.value = normalised; }
                    if (customBtn) { customBtn.style.display = ""; }
                }
            })
            .catch(function() {});
    }

    // Tab navigation.
    var tabBtns = document.querySelectorAll(".tab-btn");
    var tabPanels = document.querySelectorAll(".tab-panel");
    if (tabBtns.length > 0) {
        var savedTab = localStorage.getItem("sentinel-settings-tab");
        if (savedTab) {
            for (var i = 0; i < tabBtns.length; i++) {
                var isTarget = tabBtns[i].getAttribute("data-tab") === savedTab;
                tabBtns[i].classList.toggle("active", isTarget);
                tabBtns[i].setAttribute("aria-selected", isTarget ? "true" : "false");
            }
            for (var i = 0; i < tabPanels.length; i++) {
                tabPanels[i].classList.toggle("active", tabPanels[i].id === "tab-" + savedTab);
            }
        }

        for (var i = 0; i < tabBtns.length; i++) {
            tabBtns[i].addEventListener("click", function() {
                var tab = this.getAttribute("data-tab");
                localStorage.setItem("sentinel-settings-tab", tab);
                for (var j = 0; j < tabBtns.length; j++) {
                    tabBtns[j].classList.toggle("active", tabBtns[j] === this);
                    tabBtns[j].setAttribute("aria-selected", tabBtns[j] === this ? "true" : "false");
                }
                for (var j = 0; j < tabPanels.length; j++) {
                    tabPanels[j].classList.toggle("active", tabPanels[j].id === "tab-" + tab);
                }
            });
        }
    }

    // Load notification config.
    loadNotificationConfig();
}

function onPollIntervalChange(value) {
    var customInput = document.getElementById("poll-custom");
    var customBtn = document.getElementById("poll-custom-btn");
    if (value === "custom") {
        if (customInput) customInput.style.display = "";
        if (customBtn) customBtn.style.display = "";
        return;
    }
    if (customInput) customInput.style.display = "none";
    if (customBtn) customBtn.style.display = "none";
    setPollInterval(value);
}

function applyCustomPollInterval() {
    var input = document.getElementById("poll-custom");
    if (!input || !input.value) return;
    setPollInterval(input.value);
}

function setPollInterval(interval) {
    fetch("/api/settings/poll-interval", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ interval: interval })
    })
        .then(function (resp) {
            return resp.json().then(function (data) {
                return { ok: resp.ok, data: data };
            });
        })
        .then(function (result) {
            if (result.ok) {
                showToast(result.data.message || "Poll interval updated", "success");
            } else {
                showToast(result.data.error || "Failed to update poll interval", "error");
            }
        })
        .catch(function () {
            showToast("Network error — could not update poll interval", "error");
        });
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

function apiPost(url, body, successMsg, errorMsg, triggerEl) {
    var opts = { method: "POST" };
    if (body) {
        opts.headers = { "Content-Type": "application/json" };
        opts.body = JSON.stringify(body);
    }
    if (triggerEl) {
        triggerEl.classList.add("loading");
        triggerEl.disabled = true;
    }
    function clearLoading() {
        if (triggerEl) {
            triggerEl.classList.remove("loading");
            triggerEl.disabled = false;
        }
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
            clearLoading();
        })
        .catch(function () {
            clearLoading();
            showToast("Network error — " + errorMsg.toLowerCase(), "error");
        });
}

function approveUpdate(name, event) {
    var btn = event && event.target ? event.target.closest(".btn") : null;
    apiPost(
        "/api/approve/" + encodeURIComponent(name),
        null,
        "Approved update for " + name,
        "Failed to approve",
        btn
    );
}

function rejectUpdate(name, event) {
    var btn = event && event.target ? event.target.closest(".btn") : null;
    apiPost(
        "/api/reject/" + encodeURIComponent(name),
        null,
        "Rejected update for " + name,
        "Failed to reject",
        btn
    );
}

function triggerUpdate(name, event) {
    var btn = event && event.target ? event.target.closest(".btn") : null;
    apiPost(
        "/api/update/" + encodeURIComponent(name),
        null,
        "Update started for " + name,
        "Failed to trigger update",
        btn
    );
}

function triggerCheck(name, event) {
    var btn = event && event.target ? event.target.closest(".btn") : null;
    apiPost(
        "/api/check/" + encodeURIComponent(name),
        null,
        "Checking for updates on " + name,
        "Failed to check for updates",
        btn
    );
}

function triggerRollback(name, event) {
    var btn = event && event.target ? event.target.closest(".btn") : null;
    apiPost(
        "/api/containers/" + encodeURIComponent(name) + "/rollback",
        null,
        "Rollback started for " + name,
        "Failed to trigger rollback",
        btn
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

function triggerSelfUpdate(event) {
    var btn = event && event.target ? event.target.closest(".btn") : null;
    if (!confirm("This will restart Sentinel to apply the update. Continue?")) return;
    apiPost(
        "/api/self-update",
        null,
        "Self-update initiated — Sentinel will restart shortly",
        "Failed to trigger self-update",
        btn
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
    var expanded = !group.classList.contains("stack-collapsed");
    headerRow.setAttribute("aria-expanded", expanded ? "true" : "false");
}

function expandAllStacks() {
    var groups = document.querySelectorAll(".stack-group");
    for (var i = 0; i < groups.length; i++) {
        groups[i].classList.remove("stack-collapsed");
        var header = groups[i].querySelector(".stack-header");
        if (header) header.setAttribute("aria-expanded", "true");
    }
}

function collapseAllStacks() {
    var panels = document.querySelectorAll(".accordion-panel");
    for (var i = 0; i < panels.length; i++) {
        panels[i].style.display = "none";
        panels[i].classList.remove("accordion-open", "accordion-closing");
    }
    var groups = document.querySelectorAll(".stack-group");
    for (var i = 0; i < groups.length; i++) {
        groups[i].classList.add("stack-collapsed");
        var header = groups[i].querySelector(".stack-header");
        if (header) header.setAttribute("aria-expanded", "false");
    }
}

/* ------------------------------------------------------------
   8. Accordion (lazy-load from API)
   ------------------------------------------------------------ */

var accordionCache = {};

function toggleAccordion(name) {
    var panel = document.getElementById("accordion-" + name);
    if (!panel) return;

    var isOpen = panel.style.display !== "none" && !panel.classList.contains("accordion-closing");
    if (isOpen) {
        panel.classList.remove("accordion-open");
        panel.classList.add("accordion-closing");
        setTimeout(function() {
            panel.style.display = "none";
            panel.classList.remove("accordion-closing");
        }, 200);
        return;
    }

    panel.style.display = "";
    panel.classList.remove("accordion-closing");
    panel.classList.add("accordion-open");

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
   10. Manage Mode
   ------------------------------------------------------------ */

var manageMode = false;

function toggleManageMode() {
    manageMode = !manageMode;
    var table = document.getElementById("container-table");
    var btn = document.getElementById("manage-btn");
    if (!table || !btn) return;

    if (manageMode) {
        table.classList.add("managing");
        btn.textContent = "Done";
        btn.classList.add("active");
    } else {
        table.classList.remove("managing");
        btn.textContent = "Manage";
        btn.classList.remove("active");
        clearSelection();
    }
}

/* ------------------------------------------------------------
   11. SSE Real-time Updates
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
        if (label) {
            label.textContent = "Live";
            label.classList.add("connected");
        }
    } else {
        dot.className = "status-dot disconnected";
        dot.title = "Reconnecting\u2026";
        if (label) {
            label.textContent = "Reconnecting\u2026";
            label.classList.remove("connected");
        }
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
   12. Initialisation
   ------------------------------------------------------------ */

document.addEventListener("DOMContentLoaded", function () {
    initTheme();
    initSSE();

    // Apply stack default preference.
    var stackPref = localStorage.getItem("sentinel-stacks") || "collapsed";
    if (stackPref === "expanded") {
        expandAllStacks();
    }

    initSettingsPage();

    // Keyboard support for stack headers.
    var stackHeaders = document.querySelectorAll(".stack-header");
    for (var sh = 0; sh < stackHeaders.length; sh++) {
        stackHeaders[sh].setAttribute("tabindex", "0");
        stackHeaders[sh].setAttribute("role", "button");
        stackHeaders[sh].setAttribute("aria-expanded", "false");
        stackHeaders[sh].addEventListener("keydown", function(e) {
            if (e.key === "Enter" || e.key === " ") {
                e.preventDefault();
                toggleStack(this);
            }
        });
    }

    // Keyboard support for policy help tooltips.
    var policyHelps = document.querySelectorAll(".policy-help");
    for (var ph = 0; ph < policyHelps.length; ph++) {
        policyHelps[ph].setAttribute("role", "button");
        policyHelps[ph].setAttribute("aria-label", "Policy information");
        policyHelps[ph].addEventListener("keydown", function(e) {
            if (e.key === "Enter" || e.key === " ") {
                e.preventDefault();
                var tooltip = this.querySelector(".policy-tooltip");
                if (tooltip) {
                    var isVisible = tooltip.style.display === "block";
                    tooltip.style.display = isVisible ? "" : "block";
                }
            }
        });
    }

    // Click-to-copy for digest and mono values in accordion detail views.
    document.addEventListener("click", function(e) {
        var target = e.target.closest(".accordion-content .mono, .accordion-content .cell-digest");
        if (!target) return;
        var text = target.textContent.trim();
        if (!text || text === "\u2014") return;
        if (navigator.clipboard) {
            navigator.clipboard.writeText(text).then(function() {
                showToast("Copied to clipboard", "info");
            });
        }
    });

    // Escape key closes open tooltips.
    document.addEventListener("keydown", function(e) {
        if (e.key === "Escape") {
            var tooltips = document.querySelectorAll(".policy-tooltip");
            for (var t = 0; t < tooltips.length; t++) {
                tooltips[t].style.display = "";
            }
        }
    });

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

    // Stop/Start/Restart badge click delegation.
    document.addEventListener("click", function(e) {
        var badge = e.target.closest(".status-badge-wrap .badge-hover");
        if (!badge) return;
        e.stopPropagation();
        var wrap = badge.closest(".status-badge-wrap");
        if (!wrap) return;
        var name = wrap.getAttribute("data-name");
        if (!name) return;
        var action = wrap.getAttribute("data-action") || "restart";
        var endpoint = "/api/containers/" + encodeURIComponent(name) + "/" + action;
        var label = action.charAt(0).toUpperCase() + action.slice(1);
        apiPost(
            endpoint,
            null,
            label + " initiated for " + name,
            "Failed to " + action + " " + name
        );
    });
});

/* ------------------------------------------------------------
   13. Notification Configuration
   ------------------------------------------------------------ */

function loadNotificationConfig() {
    fetch("/api/settings/notifications")
        .then(function(r) { return r.json(); })
        .then(function(data) {
            var urlEl = document.getElementById("gotify-url");
            var tokenEl = document.getElementById("gotify-token");
            var whUrlEl = document.getElementById("webhook-url");
            var whHeadersEl = document.getElementById("webhook-headers");
            if (urlEl && data.gotify_url) urlEl.value = data.gotify_url;
            if (tokenEl && data.gotify_token) tokenEl.value = data.gotify_token;
            if (whUrlEl && data.webhook_url) whUrlEl.value = data.webhook_url;
            if (whHeadersEl && data.webhook_headers) {
                var lines = [];
                var keys = Object.keys(data.webhook_headers);
                for (var i = 0; i < keys.length; i++) {
                    lines.push(keys[i] + ": " + data.webhook_headers[keys[i]]);
                }
                whHeadersEl.value = lines.join("\n");
            }
        })
        .catch(function() {});
}

function saveNotificationConfig() {
    var gotifyUrl = (document.getElementById("gotify-url") || {}).value || "";
    var gotifyToken = (document.getElementById("gotify-token") || {}).value || "";
    var webhookUrl = (document.getElementById("webhook-url") || {}).value || "";
    var headersText = (document.getElementById("webhook-headers") || {}).value || "";

    var headers = {};
    if (headersText.trim()) {
        var lines = headersText.split("\n");
        for (var i = 0; i < lines.length; i++) {
            var line = lines[i].trim();
            if (!line) continue;
            var idx = line.indexOf(":");
            if (idx > 0) {
                headers[line.substring(0, idx).trim()] = line.substring(idx + 1).trim();
            }
        }
    }

    var btn = document.getElementById("notify-save-btn");
    if (btn) { btn.classList.add("loading"); btn.disabled = true; }

    fetch("/api/settings/notifications", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
            gotify_url: gotifyUrl,
            gotify_token: gotifyToken,
            webhook_url: webhookUrl,
            webhook_headers: headers
        })
    })
        .then(function(resp) {
            return resp.json().then(function(data) { return { ok: resp.ok, data: data }; });
        })
        .then(function(result) {
            if (result.ok) {
                showToast(result.data.message || "Notification settings saved", "success");
            } else {
                showToast(result.data.error || "Failed to save notification settings", "error");
            }
        })
        .catch(function() {
            showToast("Network error — could not save notification settings", "error");
        })
        .finally(function() {
            if (btn) { btn.classList.remove("loading"); btn.disabled = false; }
        });
}

function testNotification() {
    var btn = document.getElementById("notify-test-btn");
    apiPost(
        "/api/settings/notifications/test",
        null,
        "Test notification sent",
        "Failed to send test notification",
        btn
    );
}
