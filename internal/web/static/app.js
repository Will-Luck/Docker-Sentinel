/* ============================================================
   Docker-Sentinel Dashboard v3 — Client-side JavaScript
   ES5-compatible (no let/const/arrow functions)
   ============================================================ */

/* ------------------------------------------------------------
   0. CSRF Protection — auto-inject X-CSRF-Token on mutating requests
   ------------------------------------------------------------ */

(function () {
    var originalFetch = window.fetch;

    function getCSRFToken() {
        var match = document.cookie.match(/(^|;\s*)sentinel_csrf=([^;]+)/);
        return match ? match[2] : "";
    }

    window.fetch = function (url, opts) {
        opts = opts || {};
        var method = (opts.method || "GET").toUpperCase();
        // Only inject CSRF on state-changing methods for same-origin requests.
        if (method !== "GET" && method !== "HEAD" && method !== "OPTIONS") {
            var token = getCSRFToken();
            if (token) {
                opts.headers = opts.headers || {};
                // Support both Headers object and plain object.
                if (typeof opts.headers.set === "function") {
                    opts.headers.set("X-CSRF-Token", token);
                } else {
                    opts.headers["X-CSRF-Token"] = token;
                }
            }
        }
        return originalFetch.call(window, url, opts).then(function (resp) {
            // Auto-redirect to login on 401 (session expired).
            if (resp.status === 401 && url.indexOf("/api/auth/me") === -1) {
                window.location.href = "/login";
            }
            return resp;
        });
    };
})();

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

    // Fetch all settings and populate controls.
    fetch("/api/settings")
        .then(function(r) { return r.json(); })
        .then(function(settings) {
            // Poll interval.
            var pollSelect = document.getElementById("poll-interval");
            if (pollSelect) {
                var current = settings["SENTINEL_POLL_INTERVAL"] || settings["poll_interval"] || "6h0m0s";
                var normalised = normaliseDuration(current);
                var matched = selectOptionByValue(pollSelect, normalised);
                if (!matched && normalised !== "") {
                    pollSelect.value = "custom";
                    populateCustomDuration("poll", normalised);
                }
            }

            // Default policy.
            var policySelect = document.getElementById("default-policy");
            if (policySelect) {
                var policy = settings["default_policy"] || settings["SENTINEL_DEFAULT_POLICY"] || "manual";
                selectOptionByValue(policySelect, policy);
            }

            // Grace period.
            var graceSelect = document.getElementById("grace-period");
            if (graceSelect) {
                var grace = settings["grace_period"] || settings["SENTINEL_GRACE_PERIOD"] || "30s";
                var graceNorm = normaliseDuration(grace);
                var graceMatched = selectOptionByValue(graceSelect, graceNorm);
                if (!graceMatched && graceNorm !== "") {
                    graceSelect.value = "custom";
                    populateCustomDuration("grace", graceNorm);
                }
            }

            // Pause toggle.
            var pauseToggle = document.getElementById("pause-toggle");
            if (pauseToggle) {
                var paused = settings["paused"] === "true";
                pauseToggle.checked = paused;
                updatePauseToggleText(paused);
            }

            // Container filters.
            var filtersArea = document.getElementById("container-filters");
            if (filtersArea) {
                var filters = settings["filters"] || "";
                filtersArea.value = filters;
            }
        })
        .catch(function() {});

    // Tab navigation.
    var tabBtns = document.querySelectorAll(".tab-btn");
    var tabPanels = document.querySelectorAll(".tab-panel");
    if (tabBtns.length > 0) {
        var savedTab = localStorage.getItem("sentinel-settings-tab");
        if (savedTab) {
            // Validate saved tab still exists (tabs were renamed).
            var tabExists = false;
            for (var i = 0; i < tabBtns.length; i++) {
                if (tabBtns[i].getAttribute("data-tab") === savedTab) {
                    tabExists = true;
                    break;
                }
            }
            if (tabExists) {
                for (var i = 0; i < tabBtns.length; i++) {
                    var isTarget = tabBtns[i].getAttribute("data-tab") === savedTab;
                    tabBtns[i].classList.toggle("active", isTarget);
                    tabBtns[i].setAttribute("aria-selected", isTarget ? "true" : "false");
                }
                for (var i = 0; i < tabPanels.length; i++) {
                    tabPanels[i].classList.toggle("active", tabPanels[i].id === "tab-" + savedTab);
                }
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

    // Load notification channels.
    loadNotificationChannels();
}

function onPollIntervalChange(value) {
    var wrap = document.getElementById("poll-custom-wrap");
    if (value === "custom") {
        if (wrap) wrap.style.display = "";
        return;
    }
    if (wrap) wrap.style.display = "none";
    setPollInterval(value);
}

function applyCustomPollInterval() {
    var unit = document.getElementById("poll-custom-unit");
    var val = document.getElementById("poll-custom-value");
    if (!unit || !val || !unit.value || !val.value) return;
    setPollInterval(val.value + unit.value);
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
   2a. Settings Helpers
   ------------------------------------------------------------ */

/**
 * Normalise a Go duration string (e.g. "6h0m0s") to a compact form (e.g. "6h").
 * Strips trailing zero components.
 */
function normaliseDuration(dur) {
    return dur
        .replace("0m0s", "").replace("0s", "")
        .replace(/^0h/, "");
}

/**
 * Enable the custom duration value input when a unit is chosen.
 * prefix is "poll" or "grace".
 */
function onCustomUnitChange(prefix) {
    var unitEl = document.getElementById(prefix + "-custom-unit");
    var valEl = document.getElementById(prefix + "-custom-value");
    var btnEl = document.getElementById(prefix + "-custom-btn");
    var hasUnit = unitEl && unitEl.value !== "";
    if (valEl) {
        valEl.disabled = !hasUnit;
        if (hasUnit) valEl.focus();
    }
    if (btnEl) btnEl.disabled = !hasUnit;
}

/**
 * Parse a normalised Go duration (e.g. "45s", "2m", "6h") into {value, unit}.
 * Returns null if it can't parse.
 */
function parseDuration(dur) {
    var match = dur.match(/^(\d+)(s|m|h)$/);
    if (!match) return null;
    return { value: match[1], unit: match[2] };
}

/**
 * Populate a custom duration picker (unit select + number input) with parsed values.
 */
function populateCustomDuration(prefix, normalised) {
    var wrap = document.getElementById(prefix + "-custom-wrap");
    var unitEl = document.getElementById(prefix + "-custom-unit");
    var valEl = document.getElementById(prefix + "-custom-value");
    var btnEl = document.getElementById(prefix + "-custom-btn");
    if (!wrap) return;

    wrap.style.display = "";
    var parsed = parseDuration(normalised);
    if (parsed && unitEl && valEl) {
        unitEl.value = parsed.unit;
        valEl.value = parsed.value;
        valEl.disabled = false;
        if (btnEl) btnEl.disabled = false;
    }
}

/**
 * Select a dropdown option by value. Returns true if matched.
 */
function selectOptionByValue(selectEl, value) {
    for (var i = 0; i < selectEl.options.length; i++) {
        if (selectEl.options[i].value === value) {
            selectEl.selectedIndex = i;
            return true;
        }
    }
    return false;
}

function updatePauseToggleText(paused) {
    var text = document.getElementById("pause-toggle-text");
    if (text) {
        text.textContent = paused ? "Paused" : "Off";
    }
}

function setDefaultPolicy(value) {
    fetch("/api/settings/default-policy", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ policy: value })
    })
        .then(function(resp) {
            return resp.json().then(function(data) {
                return { ok: resp.ok, data: data };
            });
        })
        .then(function(result) {
            if (result.ok) {
                showToast(result.data.message || "Default policy updated", "success");
            } else {
                showToast(result.data.error || "Failed to update default policy", "error");
            }
        })
        .catch(function() {
            showToast("Network error — could not update default policy", "error");
        });
}

function onGracePeriodChange(value) {
    var wrap = document.getElementById("grace-custom-wrap");
    if (value === "custom") {
        if (wrap) wrap.style.display = "";
        return;
    }
    if (wrap) wrap.style.display = "none";
    setGracePeriod(value);
}

function applyCustomGracePeriod() {
    var unit = document.getElementById("grace-custom-unit");
    var val = document.getElementById("grace-custom-value");
    if (!unit || !val || !unit.value || !val.value) return;
    setGracePeriod(val.value + unit.value);
}

function setGracePeriod(duration) {
    fetch("/api/settings/grace-period", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ duration: duration })
    })
        .then(function(resp) {
            return resp.json().then(function(data) {
                return { ok: resp.ok, data: data };
            });
        })
        .then(function(result) {
            if (result.ok) {
                showToast(result.data.message || "Grace period updated", "success");
            } else {
                showToast(result.data.error || "Failed to update grace period", "error");
            }
        })
        .catch(function() {
            showToast("Network error — could not update grace period", "error");
        });
}

function setPauseState(paused) {
    updatePauseToggleText(paused);
    fetch("/api/settings/pause", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ paused: paused })
    })
        .then(function(resp) {
            return resp.json().then(function(data) {
                return { ok: resp.ok, data: data };
            });
        })
        .then(function(result) {
            if (result.ok) {
                showToast(result.data.message || (paused ? "Scanning paused" : "Scanning resumed"), "success");
            } else {
                showToast(result.data.error || "Failed to update pause state", "error");
            }
        })
        .catch(function() {
            showToast("Network error — could not update pause state", "error");
        });
}

function saveFilters() {
    var textarea = document.getElementById("container-filters");
    if (!textarea) return;

    var lines = textarea.value.split("\n");
    var patterns = [];
    for (var i = 0; i < lines.length; i++) {
        var trimmed = lines[i].replace(/^\s+|\s+$/g, "");
        if (trimmed !== "") {
            patterns.push(trimmed);
        }
    }

    fetch("/api/settings/filters", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ patterns: patterns })
    })
        .then(function(resp) {
            return resp.json().then(function(data) {
                return { ok: resp.ok, data: data };
            });
        })
        .then(function(result) {
            if (result.ok) {
                showToast(result.data.message || "Filters saved", "success");
            } else {
                showToast(result.data.error || "Failed to save filters", "error");
            }
        })
        .catch(function() {
            showToast("Network error — could not save filters", "error");
        });
}

function toggleCollapsible(headerEl) {
    var expanded = headerEl.getAttribute("aria-expanded") === "true";
    headerEl.setAttribute("aria-expanded", expanded ? "false" : "true");
    var body = headerEl.nextElementSibling;
    if (body) {
        body.style.display = expanded ? "none" : "";
    }
}

/* ------------------------------------------------------------
   2b. Dashboard Pause Banner
   ------------------------------------------------------------ */

function initPauseBanner() {
    var banner = document.getElementById("pause-banner");
    if (!banner) return;

    fetch("/api/settings")
        .then(function(r) { return r.json(); })
        .then(function(settings) {
            if (settings["paused"] === "true") {
                banner.style.display = "";
            }
        })
        .catch(function() {});
}

function resumeScanning() {
    var banner = document.getElementById("pause-banner");
    fetch("/api/settings/pause", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ paused: false })
    })
        .then(function(resp) {
            return resp.json().then(function(data) {
                return { ok: resp.ok, data: data };
            });
        })
        .then(function(result) {
            if (result.ok) {
                if (banner) banner.style.display = "none";
                showToast("Scanning resumed", "success");
            } else {
                showToast(result.data.error || "Failed to resume scanning", "error");
            }
        })
        .catch(function() {
            showToast("Network error — could not resume scanning", "error");
        });
}

function checkPauseState() {
    var banner = document.getElementById("pause-banner");
    if (!banner) return;

    fetch("/api/settings")
        .then(function(r) { return r.json(); })
        .then(function(settings) {
            banner.style.display = settings["paused"] === "true" ? "" : "none";
        })
        .catch(function() {});
}

/* ------------------------------------------------------------
   2c. Last Scan Timestamp
   ------------------------------------------------------------ */

function refreshLastScan() {
    var el = document.getElementById("last-scan");
    if (!el) return;

    fetch("/api/last-scan")
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (!data.last_scan) {
                el.textContent = "Last scan: never";
                return;
            }
            var t = new Date(data.last_scan);
            var now = new Date();
            var diff = Math.floor((now - t) / 1000);
            var ago;
            if (diff < 60) ago = "just now";
            else if (diff < 3600) ago = Math.floor(diff / 60) + "m ago";
            else if (diff < 86400) ago = Math.floor(diff / 3600) + "h ago";
            else ago = Math.floor(diff / 86400) + "d ago";
            el.textContent = "Last scan: " + ago;
            el.title = t.toLocaleString();
        })
        .catch(function() {});
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
   4a. Styled Confirmation Modal
   ------------------------------------------------------------ */

function showConfirm(title, bodyHTML) {
    return new Promise(function(resolve) {
        var triggerEl = document.activeElement;

        var overlay = document.createElement("div");
        overlay.className = "confirm-overlay";

        var modal = document.createElement("div");
        modal.className = "confirm-modal";
        modal.setAttribute("role", "dialog");
        modal.setAttribute("aria-modal", "true");

        var titleId = "confirm-title-" + Date.now();
        modal.setAttribute("aria-labelledby", titleId);

        var titleEl = document.createElement("div");
        titleEl.className = "confirm-title";
        titleEl.id = titleId;
        titleEl.textContent = title;
        modal.appendChild(titleEl);

        var body = document.createElement("div");
        body.className = "confirm-body";
        body.innerHTML = bodyHTML;
        modal.appendChild(body);

        var buttons = document.createElement("div");
        buttons.className = "confirm-buttons";

        var cancelBtn = document.createElement("button");
        cancelBtn.className = "confirm-btn-cancel";
        cancelBtn.textContent = "Cancel";
        cancelBtn.type = "button";
        buttons.appendChild(cancelBtn);

        var applyBtn = document.createElement("button");
        applyBtn.className = "confirm-btn-apply";
        applyBtn.textContent = "Apply";
        applyBtn.type = "button";
        buttons.appendChild(applyBtn);

        modal.appendChild(buttons);
        overlay.appendChild(modal);
        document.body.appendChild(overlay);

        cancelBtn.focus();

        function cleanup(result) {
            document.body.removeChild(overlay);
            if (triggerEl && triggerEl.focus) triggerEl.focus();
            resolve(result);
        }

        cancelBtn.addEventListener("click", function() { cleanup(false); });
        applyBtn.addEventListener("click", function() { cleanup(true); });

        overlay.addEventListener("click", function(e) {
            if (e.target === overlay) cleanup(false);
        });

        // Focus trap and Escape
        modal.addEventListener("keydown", function(e) {
            if (e.key === "Escape") {
                e.stopPropagation();
                cleanup(false);
                return;
            }
            if (e.key === "Tab") {
                var focusables = [cancelBtn, applyBtn];
                var idx = focusables.indexOf(document.activeElement);
                if (e.shiftKey) {
                    if (idx <= 0) { e.preventDefault(); focusables[focusables.length - 1].focus(); }
                } else {
                    if (idx >= focusables.length - 1) { e.preventDefault(); focusables[0].focus(); }
                }
            }
        });
    });
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

function triggerScan(event) {
    var btn = event && event.target ? event.target.closest(".btn") : null;
    if (btn) {
        btn.classList.add("loading");
        btn.disabled = true;
    }
    var opts = { method: "POST" };
    fetch("/api/scan", opts)
        .then(function (resp) {
            return resp.json().then(function (data) {
                return { ok: resp.ok, data: data };
            });
        })
        .then(function (result) {
            if (!result.ok) {
                showToast(result.data.error || "Failed to trigger scan", "error");
                if (btn) { btn.classList.remove("loading"); btn.disabled = false; }
            }
            // On success, keep spinner — cleared by scan_complete SSE event.
        })
        .catch(function () {
            showToast("Network error — failed to trigger scan", "error");
            if (btn) { btn.classList.remove("loading"); btn.disabled = false; }
        });
}

function triggerSelfUpdate(event) {
    var btn = event && event.target ? event.target.closest(".btn") : null;
    showConfirm("Self-Update", "<p>This will restart Sentinel to apply the update. Continue?</p>").then(function(confirmed) {
        if (!confirmed) return;
        apiPost(
            "/api/self-update",
            null,
            "Self-update initiated \u2014 Sentinel will restart shortly",
            "Failed to trigger self-update",
            btn
        );
    });
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

    // Step 1: Preview — ask the backend what would change.
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
            if (!result.ok) {
                showToast(result.data.error || "Failed to preview bulk policy change", "error");
                return;
            }

            var preview = result.data;
            var changeCount = preview.changes ? preview.changes.length : 0;
            var blockedCount = preview.blocked ? preview.blocked.length : 0;
            var unchangedCount = preview.unchanged ? preview.unchanged.length : 0;

            if (changeCount === 0) {
                var msg = "No changes to apply";
                if (blockedCount > 0) msg += " (" + blockedCount + " blocked)";
                if (unchangedCount > 0) msg += " (" + unchangedCount + " already " + policy + ")";
                showToast(msg, "info");
                return;
            }

            // Build styled preview HTML for the confirm modal.
            var bodyHTML = "";
            for (var i = 0; i < preview.changes.length; i++) {
                var c = preview.changes[i];
                bodyHTML += '<div class="confirm-change-row"><span class="confirm-change-name">' + escapeHTML(c.name) + '</span> <span class="badge badge-muted">' + escapeHTML(c.from) + '</span> \u2192 <span class="badge badge-info">' + escapeHTML(c.to) + '</span></div>';
            }
            if (blockedCount > 0) {
                bodyHTML += '<div class="confirm-section-label">Blocked (' + blockedCount + ')</div>';
                for (var b = 0; b < preview.blocked.length; b++) {
                    bodyHTML += '<div class="confirm-muted-row">' + escapeHTML(preview.blocked[b].name) + ' \u2014 ' + escapeHTML(preview.blocked[b].reason) + '</div>';
                }
            }
            if (unchangedCount > 0) {
                bodyHTML += '<div class="confirm-section-label">Unchanged (' + unchangedCount + ')</div>';
                for (var u = 0; u < preview.unchanged.length; u++) {
                    bodyHTML += '<div class="confirm-muted-row">' + escapeHTML(preview.unchanged[u].name) + ' \u2014 ' + escapeHTML(preview.unchanged[u].reason) + '</div>';
                }
            }

            // Step 2: User confirms via styled modal.
            var confirmTitle = "Change policy to \u2018" + escapeHTML(policy) + "\u2019 for " + changeCount + " container" + (changeCount !== 1 ? "s" : "") + "?";
            showConfirm(confirmTitle, bodyHTML).then(function(confirmed) {
                if (!confirmed) return;

                // Step 3: Confirm — send with confirm: true to apply.
                fetch("/api/bulk/policy", {
                    method: "POST",
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify({ containers: names, policy: policy, confirm: true })
                })
                    .then(function (resp2) {
                        return resp2.json().then(function (data2) {
                            return { ok: resp2.ok, data: data2 };
                        });
                    })
                    .then(function (confirmResult) {
                        if (!confirmResult.ok) {
                            showToast(confirmResult.data.error || "Failed to apply bulk policy change", "error");
                            return;
                        }

                        var applied = confirmResult.data.applied || 0;
                        showToast("Policy changed to '" + policy + "' for " + applied + " container" + (applied !== 1 ? "s" : ""), "success");

                        // Refresh affected rows to reflect new policy values.
                        for (var r = 0; r < preview.changes.length; r++) {
                            updateContainerRow(preview.changes[r].name);
                        }

                        // Clear selection and exit manage mode.
                        clearSelection();
                        if (manageMode) toggleManageMode();
                    })
                    .catch(function () {
                        showToast("Network error — could not apply bulk policy change", "error");
                    });
            });
        })
        .catch(function () {
            showToast("Network error — could not preview bulk policy change", "error");
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
    if (e.target.closest(".status-badge-wrap")) {
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
    var stackCbs = document.querySelectorAll(".stack-select");
    for (var s = 0; s < stackCbs.length; s++) {
        stackCbs[s].checked = false;
        stackCbs[s].indeterminate = false;
    }
    updateSelectionUI();
}

function recomputeSelectionState() {
    // Per-stack: set stack checkbox to checked/unchecked/indeterminate.
    var stacks = document.querySelectorAll("tbody.stack-group");
    for (var s = 0; s < stacks.length; s++) {
        var stackCb = stacks[s].querySelector(".stack-select");
        if (!stackCb) continue;
        var rows = stacks[s].querySelectorAll(".row-select");
        var total = rows.length;
        var checked = 0;
        for (var r = 0; r < rows.length; r++) {
            if (rows[r].checked) checked++;
        }
        stackCb.checked = total > 0 && checked === total;
        stackCb.indeterminate = checked > 0 && checked < total;
    }

    // Global select-all.
    var allRows = document.querySelectorAll(".row-select");
    var allTotal = allRows.length;
    var allChecked = 0;
    for (var a = 0; a < allRows.length; a++) {
        if (allRows[a].checked) allChecked++;
    }
    var selectAll = document.getElementById("select-all");
    if (selectAll) {
        selectAll.checked = allTotal > 0 && allChecked === allTotal;
        selectAll.indeterminate = allChecked > 0 && allChecked < allTotal;
    }

    updateSelectionUI();
}

/* ------------------------------------------------------------
   9a. Filter & Sort System
   ------------------------------------------------------------ */

var filterState = { status: "all", updates: "all", sort: "default" };

function initFilters() {
    var saved = localStorage.getItem("sentinel-filters");
    if (saved) { try { filterState = JSON.parse(saved); } catch(e) {} }

    // Restore active states from saved filter state.
    var pills = document.querySelectorAll(".filter-pill");
    for (var i = 0; i < pills.length; i++) {
        var p = pills[i];
        if (filterState[p.getAttribute("data-filter")] === p.getAttribute("data-value")) {
            p.classList.add("active");
        }
    }

    var bar = document.getElementById("filter-bar");
    if (bar) bar.addEventListener("click", function(e) {
        var pill = e.target.closest(".filter-pill");
        if (!pill) return;

        var key = pill.getAttribute("data-filter");
        var value = pill.getAttribute("data-value");
        var exclusive = pill.getAttribute("data-exclusive");
        var wasActive = pill.classList.contains("active");

        // Deactivate exclusive siblings.
        if (exclusive) {
            var siblings = bar.querySelectorAll('.filter-pill[data-exclusive="' + exclusive + '"]');
            for (var s = 0; s < siblings.length; s++) siblings[s].classList.remove("active");
        }

        // Toggle: if was active, deactivate (reset to default); if not, activate.
        if (wasActive) {
            pill.classList.remove("active");
            filterState[key] = key === "sort" ? "default" : "all";
        } else {
            pill.classList.add("active");
            filterState[key] = value;
        }

        localStorage.setItem("sentinel-filters", JSON.stringify(filterState));
        applyFiltersAndSort();
    });

    applyFiltersAndSort();
}

function applyFiltersAndSort() {
    var table = document.getElementById("container-table");
    if (!table) return;
    var stacks = table.querySelectorAll("tbody.stack-group");

    for (var s = 0; s < stacks.length; s++) {
        var stack = stacks[s];
        var rows = stack.querySelectorAll(".container-row");
        var visibleCount = 0;
        for (var r = 0; r < rows.length; r++) {
            var row = rows[r];
            var show = true;
            if (filterState.status === "running") show = row.classList.contains("state-running");
            else if (filterState.status === "stopped") show = !row.classList.contains("state-running");
            if (show && filterState.updates === "pending") show = row.classList.contains("has-update");
            row.style.display = show ? "" : "none";
            var next = row.nextElementSibling;
            if (next && next.classList.contains("accordion-panel")) {
                if (!show) next.style.display = "none";
            }
            if (show) visibleCount++;
        }
        stack.style.display = visibleCount === 0 ? "none" : "";
    }

    if (filterState.sort !== "default") {
        sortRows(stacks);
    }
}

function sortRows(stacks) {
    for (var s = 0; s < stacks.length; s++) {
        var tbody = stacks[s];
        var rows = [];
        var rowEls = tbody.querySelectorAll(".container-row");
        for (var r = 0; r < rowEls.length; r++) {
            if (rowEls[r].style.display === "none") continue;
            rows.push(rowEls[r]);
        }

        if (filterState.sort === "alpha") {
            rows.sort(function(a, b) {
                return a.getAttribute("data-name").localeCompare(b.getAttribute("data-name"));
            });
        } else if (filterState.sort === "status") {
            rows.sort(function(a, b) {
                var sa = statusScore(a), sb = statusScore(b);
                return sa !== sb ? sb - sa : a.getAttribute("data-name").localeCompare(b.getAttribute("data-name"));
            });
        }

        for (var i = 0; i < rows.length; i++) {
            var row = rows[i];
            var acc = document.getElementById("accordion-" + row.getAttribute("data-name"));
            tbody.appendChild(row);
            if (acc) tbody.appendChild(acc);
        }
    }
}

function statusScore(row) {
    if (!row.classList.contains("state-running")) return 3;
    if (row.classList.contains("has-update")) return 2;
    return 1;
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

/* ------------------------------------------------------------
   11a. Live Row Updates — targeted DOM patching via partial endpoint
   ------------------------------------------------------------ */

function updateContainerRow(name) {
    var enc = encodeURIComponent(name);
    fetch("/api/containers/" + enc + "/row")
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (!data.html) return;

            // Update stats counters.
            updateStats(data.total, data.running, data.pending);

            // Parse the server-rendered HTML (from Go templates, not user input)
            // into DOM nodes using a temporary tbody element.
            var temp = document.createElement("tbody");
            temp.innerHTML = data.html; // Safe: server-rendered Go template HTML, no user content

            var oldRow = document.querySelector('tr.container-row[data-name="' + name + '"]');
            var oldAccordion = document.getElementById("accordion-" + name);

            if (oldRow) {
                var newRow = temp.querySelector(".container-row");
                var newAccordion = temp.querySelector(".accordion-panel");

                if (newRow) {
                    oldRow.replaceWith(newRow);
                    newRow.classList.add("row-updated");
                }
                if (newAccordion && oldAccordion) {
                    oldAccordion.replaceWith(newAccordion);
                }

                // Reapply checkbox state from selectedContainers after DOM patch.
                var newCb = newRow ? newRow.querySelector(".row-select") : null;
                if (newCb && selectedContainers[newCb.value]) {
                    newCb.checked = true;
                }
                recomputeSelectionState();
            }

            // Clear accordion cache for this container.
            delete accordionCache[name];

            // Reapply filters after DOM patch.
            applyFiltersAndSort();
        })
        .catch(function() {
            // Fallback: full reload on error.
            scheduleReload();
        });
}

function updateStats(total, running, pending) {
    var stats = document.getElementById("stats");
    if (!stats) return;
    var values = stats.querySelectorAll(".stat-value");
    if (values[0]) values[0].textContent = total;
    if (values[1]) values[1].textContent = running;
    if (values[2]) values[2].textContent = pending;
}

function showBadgeSpinner(wrap) {
    var defaultBadge = wrap.querySelector(".badge-default");
    var hoverBadge = wrap.querySelector(".badge-hover");
    if (defaultBadge) defaultBadge.style.display = "none";
    if (hoverBadge) hoverBadge.style.display = "none";

    var spinner = document.createElement("span");
    spinner.className = "badge-loading";
    wrap.appendChild(spinner);
    wrap.style.pointerEvents = "none";
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
            if (data.container_name) {
                updateContainerRow(data.container_name);
                return;
            }
        } catch (_) {}
        scheduleReload();
    });

    es.addEventListener("container_state", function (e) {
        try {
            var data = JSON.parse(e.data);
            if (data.container_name) {
                updateContainerRow(data.container_name);
                return;
            }
        } catch (_) {}
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
        // Clear scan button spinner.
        var scanBtn = document.getElementById("scan-btn");
        if (scanBtn) { scanBtn.classList.remove("loading"); scanBtn.disabled = false; }
        // Re-check pause state on scan events.
        checkPauseState();
        refreshLastScan();
        scheduleReload();
    });

    es.addEventListener("settings_change", function () {
        checkPauseState();
    });

    es.addEventListener("policy_change", function (e) {
        try {
            var data = JSON.parse(e.data);
            showToast(data.message || ("Policy changed: " + data.container_name), "info");
            if (data.container_name) {
                updateContainerRow(data.container_name);
                return;
            }
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
    initPauseBanner();
    initFilters();
    refreshLastScan();

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
                    checkboxes[i].checked = target.checked;
                    selectedContainers[checkboxes[i].value] = target.checked;
                }
                recomputeSelectionState();
                return;
            }

            // Stack-level select checkbox
            if (target.classList.contains("stack-select")) {
                var tbody = target.closest("tbody.stack-group");
                if (tbody) {
                    var rows = tbody.querySelectorAll(".row-select");
                    for (var i = 0; i < rows.length; i++) {
                        rows[i].checked = target.checked;
                        selectedContainers[rows[i].value] = target.checked;
                    }
                }
                recomputeSelectionState();
                return;
            }

            // Individual row checkbox
            if (target.classList.contains("row-select")) {
                selectedContainers[target.value] = target.checked;
                recomputeSelectionState();
            }
        });

        // Prevent stack-select checkbox from toggling stack collapse.
        table.addEventListener("click", function(e) {
            if (e.target.classList.contains("stack-select")) {
                e.stopPropagation();
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

        // Show inline spinner immediately.
        showBadgeSpinner(wrap);

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
   13. Notification Configuration — Multi-channel System
   ------------------------------------------------------------ */

var EVENT_TYPES = [
    { key: "update_available", label: "Update Available" },
    { key: "update_started", label: "Update Started" },
    { key: "update_succeeded", label: "Update Succeeded" },
    { key: "update_failed", label: "Update Failed" },
    { key: "rollback_succeeded", label: "Rollback Succeeded" },
    { key: "rollback_failed", label: "Rollback Failed" },
    { key: "container_state", label: "State Change" }
];

// Map legacy event keys (from older saved configs in BoltDB) to current constants.
var LEGACY_EVENT_KEYS = {
    "update_complete": "update_succeeded",
    "rollback": "rollback_succeeded",
    "state_change": "container_state"
};

function canonicaliseEventKey(key) {
    return LEGACY_EVENT_KEYS[key] || key;
}

var PROVIDER_FIELDS = {
    gotify: [
        { key: "url", label: "Server URL", type: "text", placeholder: "http://gotify:80" },
        { key: "token", label: "App Token", type: "password", placeholder: "Token" }
    ],
    webhook: [
        { key: "url", label: "URL", type: "text", placeholder: "https://example.com/webhook" },
        { key: "headers", label: "Headers (JSON)", type: "text", placeholder: '{"Authorization": "Bearer ..."}' }
    ],
    slack: [
        { key: "webhook_url", label: "Webhook URL", type: "text", placeholder: "https://hooks.slack.com/services/..." }
    ],
    discord: [
        { key: "webhook_url", label: "Webhook URL", type: "text", placeholder: "https://discord.com/api/webhooks/..." }
    ],
    ntfy: [
        { key: "server", label: "Server", type: "text", placeholder: "https://ntfy.sh" },
        { key: "topic", label: "Topic", type: "text", placeholder: "sentinel" },
        { key: "priority", label: "Priority", type: "text", placeholder: "3" }
    ],
    telegram: [
        { key: "bot_token", label: "Bot Token", type: "password", placeholder: "123456:ABC-DEF..." },
        { key: "chat_id", label: "Chat ID", type: "text", placeholder: "-1001234567890" }
    ],
    pushover: [
        { key: "app_token", label: "App Token", type: "password", placeholder: "Application token" },
        { key: "user_key", label: "User Key", type: "password", placeholder: "User/group key" }
    ]
};

var notificationChannels = [];

function loadNotificationChannels() {
    fetch("/api/settings/notifications")
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (Array.isArray(data)) {
                notificationChannels = data;
            } else {
                notificationChannels = [];
            }
            renderChannels();
        })
        .catch(function() {
            notificationChannels = [];
            renderChannels();
        });
}

function renderChannels() {
    var container = document.getElementById("channel-list");
    if (!container) return;

    while (container.firstChild) container.removeChild(container.firstChild);

    if (notificationChannels.length === 0) {
        var empty = document.createElement("div");
        empty.className = "empty-state";
        empty.style.padding = "var(--sp-8) var(--sp-4)";
        var emptyH = document.createElement("h3");
        emptyH.textContent = "No notification channels";
        var emptyP = document.createElement("p");
        emptyP.textContent = "Add a channel using the dropdown below to receive update notifications.";
        empty.appendChild(emptyH);
        empty.appendChild(emptyP);
        container.appendChild(empty);
        return;
    }

    for (var i = 0; i < notificationChannels.length; i++) {
        container.appendChild(buildChannelCard(i));
    }
}

function buildChannelCard(index) {
    var ch = notificationChannels[index];
    var fields = PROVIDER_FIELDS[ch.type] || [];
    var settings = {};
    try { settings = JSON.parse(ch.settings || "{}"); } catch(e) { settings = ch.settings || {}; }

    var card = document.createElement("div");
    card.className = "channel-card";
    card.setAttribute("data-index", index);

    // Header: type badge + name input + enabled toggle + actions
    var header = document.createElement("div");
    header.className = "channel-card-header";

    var badge = document.createElement("span");
    badge.className = "channel-type-badge";
    badge.textContent = ch.type;
    header.appendChild(badge);

    var nameInput = document.createElement("input");
    nameInput.className = "channel-name-input";
    nameInput.type = "text";
    nameInput.value = ch.name || "";
    nameInput.placeholder = "Channel name";
    nameInput.setAttribute("data-field", "name");
    header.appendChild(nameInput);

    var actions = document.createElement("div");
    actions.className = "channel-actions";

    var toggleLabel = document.createElement("label");
    toggleLabel.style.fontSize = "0.75rem";
    toggleLabel.style.color = "var(--fg-secondary)";
    toggleLabel.style.display = "flex";
    toggleLabel.style.alignItems = "center";
    toggleLabel.style.gap = "6px";

    var toggle = document.createElement("input");
    toggle.type = "checkbox";
    toggle.className = "channel-toggle";
    toggle.checked = ch.enabled !== false;
    toggle.setAttribute("data-field", "enabled");
    toggleLabel.appendChild(toggle);
    toggleLabel.appendChild(document.createTextNode("Enabled"));
    actions.appendChild(toggleLabel);

    var testBtn = document.createElement("button");
    testBtn.className = "btn";
    testBtn.textContent = "Test";
    testBtn.setAttribute("data-index", index);
    testBtn.onclick = function() { testChannel(parseInt(this.getAttribute("data-index"))); };
    actions.appendChild(testBtn);

    var delBtn = document.createElement("button");
    delBtn.className = "btn btn-error";
    delBtn.textContent = "Delete";
    delBtn.setAttribute("data-index", index);
    delBtn.onclick = function() { deleteChannel(parseInt(this.getAttribute("data-index"))); };
    actions.appendChild(delBtn);

    header.appendChild(actions);
    card.appendChild(header);

    // Provider-specific fields
    var fieldsDiv = document.createElement("div");
    fieldsDiv.className = "channel-fields";

    for (var f = 0; f < fields.length; f++) {
        var field = fields[f];
        var row = document.createElement("div");
        row.className = "channel-field";

        var label = document.createElement("div");
        label.className = "channel-field-label";
        label.textContent = field.label;
        row.appendChild(label);

        var input = document.createElement("input");
        input.className = "channel-field-input";
        input.type = field.type || "text";
        input.placeholder = field.placeholder || "";
        input.setAttribute("data-setting", field.key);

        // Special handling for headers (object -> JSON string)
        var val = settings[field.key];
        if (field.key === "headers" && val && typeof val === "object") {
            input.value = JSON.stringify(val);
        } else if (field.key === "priority" && val !== undefined) {
            input.value = String(val);
        } else {
            input.value = val || "";
        }

        row.appendChild(input);
        fieldsDiv.appendChild(row);
    }

    card.appendChild(fieldsDiv);

    // Event filter pills.
    var enabledEvents = ch.events;
    // If events is null/undefined/empty, all events are enabled by default.
    var allEnabled = !enabledEvents || enabledEvents.length === 0;

    // Canonicalise legacy event keys from saved config so pills light up correctly.
    var canonicalEvents = [];
    if (!allEnabled) {
        for (var ce = 0; ce < enabledEvents.length; ce++) {
            canonicalEvents.push(canonicaliseEventKey(enabledEvents[ce]));
        }
    }

    var pillsWrap = document.createElement("div");
    pillsWrap.className = "event-pills";

    var pillsLabel = document.createElement("div");
    pillsLabel.className = "event-pills-label";
    pillsLabel.textContent = "Event filters";
    pillsWrap.appendChild(pillsLabel);

    for (var e = 0; e < EVENT_TYPES.length; e++) {
        var evtType = EVENT_TYPES[e];
        var pill = document.createElement("button");
        pill.type = "button";
        pill.className = "event-pill";
        pill.textContent = evtType.label;
        pill.setAttribute("data-event", evtType.key);

        var isActive = allEnabled;
        if (!allEnabled) {
            for (var k = 0; k < canonicalEvents.length; k++) {
                if (canonicalEvents[k] === evtType.key) {
                    isActive = true;
                    break;
                }
            }
        }
        if (isActive) {
            pill.classList.add("active");
        }

        pill.addEventListener("click", function() {
            this.classList.toggle("active");
        });

        pillsWrap.appendChild(pill);
    }

    card.appendChild(pillsWrap);
    return card;
}

function addChannel() {
    var select = document.getElementById("channel-type-select");
    if (!select || !select.value) return;

    var type = select.value;
    var name = type.charAt(0).toUpperCase() + type.slice(1);

    var defaultEvents = [];
    for (var i = 0; i < EVENT_TYPES.length; i++) {
        defaultEvents.push(EVENT_TYPES[i].key);
    }

    notificationChannels.push({
        id: "new-" + Date.now() + "-" + Math.random().toString(36).substr(2, 6),
        type: type,
        name: name,
        enabled: true,
        settings: "{}",
        events: defaultEvents
    });

    select.value = "";
    renderChannels();
    showToast("Added " + name + " channel — configure and save", "info");
}

function deleteChannel(index) {
    if (index < 0 || index >= notificationChannels.length) return;
    var name = notificationChannels[index].name || notificationChannels[index].type;
    notificationChannels.splice(index, 1);
    renderChannels();
    showToast("Removed " + name + " — save to apply", "info");
}

function collectChannelsFromDOM() {
    var cards = document.querySelectorAll(".channel-card");
    for (var i = 0; i < cards.length; i++) {
        var idx = parseInt(cards[i].getAttribute("data-index"));
        if (idx < 0 || idx >= notificationChannels.length) continue;

        var nameInput = cards[i].querySelector('[data-field="name"]');
        if (nameInput) notificationChannels[idx].name = nameInput.value;

        var toggle = cards[i].querySelector('[data-field="enabled"]');
        if (toggle) notificationChannels[idx].enabled = toggle.checked;

        var settings = {};
        try { settings = JSON.parse(notificationChannels[idx].settings || "{}"); } catch(e) { settings = {}; }
        if (typeof notificationChannels[idx].settings === "object" && notificationChannels[idx].settings !== null && !(notificationChannels[idx].settings instanceof String)) {
            settings = notificationChannels[idx].settings;
        }

        var inputs = cards[i].querySelectorAll("[data-setting]");
        for (var j = 0; j < inputs.length; j++) {
            var key = inputs[j].getAttribute("data-setting");
            var val = inputs[j].value;
            if (key === "headers") {
                try { settings[key] = JSON.parse(val); } catch(e) { settings[key] = {}; }
            } else if (key === "priority") {
                settings[key] = parseInt(val) || 3;
            } else {
                settings[key] = val;
            }
        }

        notificationChannels[idx].settings = JSON.stringify(settings);

        // Collect event pill states.
        var pills = cards[i].querySelectorAll(".event-pill");
        var events = [];
        for (var p = 0; p < pills.length; p++) {
            if (pills[p].classList.contains("active")) {
                events.push(pills[p].getAttribute("data-event"));
            }
        }
        notificationChannels[idx].events = events;
    }
}

function saveNotificationChannels() {
    collectChannelsFromDOM();

    var btn = document.getElementById("notify-save-btn");
    if (btn) { btn.classList.add("loading"); btn.disabled = true; }

    // Parse settings strings back to objects for the API.
    var payload = [];
    for (var i = 0; i < notificationChannels.length; i++) {
        var ch = {};
        var keys = Object.keys(notificationChannels[i]);
        for (var k = 0; k < keys.length; k++) {
            ch[keys[k]] = notificationChannels[i][keys[k]];
        }
        if (typeof ch.settings === "string") {
            try { ch.settings = JSON.parse(ch.settings); } catch(e) { ch.settings = {}; }
        }
        payload.push(ch);
    }

    fetch("/api/settings/notifications", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload)
    })
        .then(function(resp) {
            return resp.json().then(function(data) { return { ok: resp.ok, data: data }; });
        })
        .then(function(result) {
            if (result.ok) {
                showToast(result.data.message || "Notification settings saved", "success");
                loadNotificationChannels();
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

function testChannel(index) {
    collectChannelsFromDOM();
    var ch = notificationChannels[index];
    if (!ch) return;

    var btn = document.querySelectorAll('.channel-card[data-index="' + index + '"] .btn')[0];

    apiPost(
        "/api/settings/notifications/test",
        { id: ch.id },
        "Test sent to " + (ch.name || ch.type),
        "Test failed for " + (ch.name || ch.type),
        btn
    );
}

function testNotification() {
    var btn = document.getElementById("notify-test-btn");
    apiPost(
        "/api/settings/notifications/test",
        null,
        "Test notification sent to all channels",
        "Failed to send test notification",
        btn
    );
}
