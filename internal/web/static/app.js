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
   1b. Accordion Persistence
   ------------------------------------------------------------ */

function getAccordionKey(details) {
    var h2 = details.querySelector("h2");
    if (!h2) return null;
    var page = document.body.dataset.page || window.location.pathname;
    return "sentinel-acc::" + page + "::" + h2.textContent.trim();
}

function saveAccordionState(details) {
    var key = getAccordionKey(details);
    if (!key) return;
    localStorage.setItem(key, details.open ? "1" : "0");
}

function clearAccordionState() {
    var keys = [];
    for (var i = 0; i < localStorage.length; i++) {
        var k = localStorage.key(i);
        if (k && k.indexOf("sentinel-acc::") === 0) keys.push(k);
    }
    for (var j = 0; j < keys.length; j++) {
        localStorage.removeItem(keys[j]);
    }
}

function initAccordionPersistence() {
    var mode = localStorage.getItem("sentinel-sections") || "remember";
    var accordions = document.querySelectorAll("details.accordion");

    // If there's only one accordion on the page, always expand it.
    var forceOpen = accordions.length === 1;

    for (var i = 0; i < accordions.length; i++) {
        var details = accordions[i];

        if (forceOpen) {
            details.open = true;
        } else if (mode === "remember") {
            // Restore saved state if available
            var key = getAccordionKey(details);
            if (key) {
                var saved = localStorage.getItem(key);
                if (saved === "1") details.open = true;
                else if (saved === "0") details.open = false;
                // If no saved state, keep the HTML default
            }
        } else if (mode === "collapsed") {
            details.open = false;
        } else if (mode === "expanded") {
            details.open = true;
        }

        // Listen for toggle to persist user choices (always, regardless of mode)
        details.addEventListener("toggle", function() {
            if (localStorage.getItem("sentinel-sections") === "remember") {
                saveAccordionState(this);
            }
        });
    }
}

/* ------------------------------------------------------------
   2. Settings Page
   ------------------------------------------------------------ */

function initSettingsPage() {
    var themeSelect = document.getElementById("theme-select");
    var stackSelect = document.getElementById("stack-default");
    var sectionSelect = document.getElementById("section-default");
    if (!themeSelect) return;

    themeSelect.value = localStorage.getItem("sentinel-theme") || "auto";
    stackSelect.value = localStorage.getItem("sentinel-stacks") || "collapsed";
    if (sectionSelect) sectionSelect.value = localStorage.getItem("sentinel-sections") || "remember";

    themeSelect.addEventListener("change", function() {
        applyTheme(themeSelect.value);
        localStorage.setItem("sentinel-theme", themeSelect.value);
        showToast("Theme updated", "success");
    });
    stackSelect.addEventListener("change", function() {
        localStorage.setItem("sentinel-stacks", stackSelect.value);
        showToast("Stack default updated", "success");
    });
    if (sectionSelect) {
        sectionSelect.addEventListener("change", function() {
            localStorage.setItem("sentinel-sections", sectionSelect.value);
            if (sectionSelect.value !== "remember") {
                // Clear saved accordion state when switching away from "remember"
                clearAccordionState();
            }
            showToast("Section default updated", "success");
        });
    }

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

            // Rollback policy.
            var rbSelect = document.getElementById("rollback-policy");
            if (rbSelect) {
                var rbPolicy = settings["rollback_policy"] || settings["SENTINEL_ROLLBACK_POLICY"] || "";
                selectOptionByValue(rbSelect, rbPolicy);
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

            // Latest auto-update toggle.
            var latestAutoToggle = document.getElementById("latest-auto-toggle");
            if (latestAutoToggle) {
                var latestAuto = settings["latest_auto_update"] !== "false";
                latestAutoToggle.checked = latestAuto;
                updateLatestAutoText(latestAuto);
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

            // Image cleanup toggle.
            var imageCleanupToggle = document.getElementById("image-cleanup-toggle");
            if (imageCleanupToggle) {
                var imageCleanup = settings["image_cleanup"] === "true";
                imageCleanupToggle.checked = imageCleanup;
                updateToggleText("image-cleanup-text", imageCleanup);
            }

            // Cron schedule.
            var cronInput = document.getElementById("cron-schedule");
            if (cronInput) {
                cronInput.value = settings["schedule"] || "";
            }

            // Dependency-aware toggle.
            var depAwareToggle = document.getElementById("dep-aware-toggle");
            if (depAwareToggle) {
                var depAware = settings["dependency_aware"] === "true";
                depAwareToggle.checked = depAware;
                updateToggleText("dep-aware-text", depAware);
            }

            // Hooks enabled toggle.
            var hooksToggle = document.getElementById("hooks-toggle");
            if (hooksToggle) {
                var hooksEnabled = settings["hooks_enabled"] === "true";
                hooksToggle.checked = hooksEnabled;
                updateToggleText("hooks-toggle-text", hooksEnabled);
            }

            // Write hook labels toggle.
            var hooksLabelsToggle = document.getElementById("hooks-labels-toggle");
            if (hooksLabelsToggle) {
                var hooksLabels = settings["hooks_write_labels"] === "true";
                hooksLabelsToggle.checked = hooksLabels;
                updateToggleText("hooks-labels-text", hooksLabels);
            }
        })
        .catch(function() { /* ignore — falls back to defaults */ });

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

    // Load digest settings and per-container notification preferences.
    loadDigestSettings();
    loadContainerNotifyPrefs();

    // Load registry credentials and rate limits.
    loadRegistries();

    // Load GHCR alternatives table on registries tab.
    renderGHCRAlternatives();

    // Load About info.
    loadAboutInfo();

    // Load cluster settings.
    loadClusterSettings();
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

function setRollbackPolicy(value) {
    fetch("/api/settings/rollback-policy", {
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
                showToast(result.data.message || "Rollback policy updated", "success");
            } else {
                showToast(result.data.error || "Failed to update rollback policy", "error");
            }
        })
        .catch(function() {
            showToast("Network error — could not update rollback policy", "error");
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

function updateLatestAutoText(enabled) {
    var text = document.getElementById("latest-auto-text");
    if (text) {
        text.textContent = enabled ? "On" : "Off";
    }
}

function setLatestAutoUpdate(enabled) {
    updateLatestAutoText(enabled);
    fetch("/api/settings/latest-auto-update", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled: enabled })
    })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            showToast(data.message || "Setting updated", "success");
        })
        .catch(function() {
            showToast("Network error — could not update setting", "error");
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

/* ------------------------------------------------------------
   2b. Cluster Settings Tab
   ------------------------------------------------------------ */

function loadClusterSettings() {
    // Only run on the settings page (cluster-enabled element exists).
    if (!document.getElementById("cluster-enabled")) return;

    fetch("/api/settings/cluster")
        .then(function(r) { return r.json(); })
        .then(function(s) {
            var enabled = s.enabled === "true";
            document.getElementById("cluster-enabled").checked = enabled;
            updateToggleText("cluster-enabled-text", enabled);
            document.getElementById("cluster-port").value = s.port || "9443";
            document.getElementById("cluster-grace").value = s.grace_period || "30m";
            document.getElementById("cluster-policy").value = s.remote_policy || "manual";
            toggleClusterFields(enabled);
        })
        .catch(function(err) {
            console.error("Failed to load cluster settings:", err);
        });
}

function onClusterToggle(enabled) {
    if (!enabled) {
        if (!confirm("Disabling cluster mode will disconnect all agents. Continue?")) {
            document.getElementById("cluster-enabled").checked = true;
            return;
        }
    }
    updateToggleText("cluster-enabled-text", enabled);
    toggleClusterFields(enabled);
    saveClusterSettings();
}

function toggleClusterFields(enabled) {
    var fields = document.getElementById("cluster-fields");
    if (!fields) return;
    if (enabled) {
        fields.classList.remove("disabled");
    } else {
        fields.classList.add("disabled");
    }
}

function saveClusterSettings() {
    var enabled = document.getElementById("cluster-enabled").checked;
    fetch("/api/settings/cluster", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
            enabled: enabled,
            port: document.getElementById("cluster-port").value,
            grace_period: document.getElementById("cluster-grace").value,
            remote_policy: document.getElementById("cluster-policy").value
        })
    })
        .then(function(resp) {
            return resp.json().then(function(data) {
                return { ok: resp.ok, data: data };
            });
        })
        .then(function(result) {
            if (result.ok) {
                showToast("Cluster settings saved", "success");
            } else {
                showToast(result.data.error || "Failed to save cluster settings", "error");
            }
        })
        .catch(function() {
            showToast("Network error — could not save cluster settings", "error");
        });
}

function updateToggleText(textId, enabled) {
    var text = document.getElementById(textId);
    if (text) {
        text.textContent = enabled ? "On" : "Off";
    }
}

function setImageCleanup(enabled) {
    updateToggleText("image-cleanup-text", enabled);
    fetch("/api/settings/image-cleanup", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled: enabled })
    })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            showToast(data.message || "Setting updated", "success");
        })
        .catch(function() {
            showToast("Network error — could not update setting", "error");
        });
}

function saveCronSchedule() {
    var input = document.getElementById("cron-schedule");
    if (!input) return;
    fetch("/api/settings/schedule", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ schedule: input.value })
    })
        .then(function(resp) {
            return resp.json().then(function(data) {
                return { ok: resp.ok, data: data };
            });
        })
        .then(function(result) {
            if (result.ok) {
                showToast(result.data.message || "Schedule updated", "success");
            } else {
                showToast(result.data.error || "Failed to update schedule", "error");
            }
        })
        .catch(function() {
            showToast("Network error — could not update schedule", "error");
        });
}

function setDependencyAware(enabled) {
    updateToggleText("dep-aware-text", enabled);
    fetch("/api/settings/dependency-aware", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled: enabled })
    })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            showToast(data.message || "Setting updated", "success");
        })
        .catch(function() {
            showToast("Network error — could not update setting", "error");
        });
}

function setHooksEnabled(enabled) {
    updateToggleText("hooks-toggle-text", enabled);
    fetch("/api/settings/hooks-enabled", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled: enabled })
    })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            showToast(data.message || "Setting updated", "success");
        })
        .catch(function() {
            showToast("Network error — could not update setting", "error");
        });
}

function setHooksWriteLabels(enabled) {
    updateToggleText("hooks-labels-text", enabled);
    fetch("/api/settings/hooks-write-labels", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled: enabled })
    })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            showToast(data.message || "Setting updated", "success");
        })
        .catch(function() {
            showToast("Network error — could not update setting", "error");
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
        .catch(function() { /* ignore — falls back to defaults */ });
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
        .catch(function() { /* ignore — falls back to defaults */ });
}

/* ------------------------------------------------------------
   2c. Last Scan Timestamp
   ------------------------------------------------------------ */

var lastScanTimestamp = null;
var lastScanTimer = null;
var ghcrAlternatives = {};

function refreshLastScan() {
    var el = document.getElementById("last-scan");
    if (!el) return;

    fetch("/api/last-scan")
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (!data.last_scan) {
                el.textContent = "Last scan: never";
                lastScanTimestamp = null;
                return;
            }
            lastScanTimestamp = new Date(data.last_scan);
            el.title = lastScanTimestamp.toLocaleString();
            renderLastScanTicker();
            if (!lastScanTimer) {
                lastScanTimer = setInterval(renderLastScanTicker, 1000);
            }
        })
        .catch(function() { /* ignore — falls back to defaults */ });
}

function renderLastScanTicker() {
    var el = document.getElementById("last-scan");
    if (!el || !lastScanTimestamp) return;

    var diff = Math.floor((Date.now() - lastScanTimestamp.getTime()) / 1000);
    if (diff < 0) diff = 0;

    var parts = [];
    if (diff >= 86400) {
        parts.push(Math.floor(diff / 86400) + "d");
        diff %= 86400;
    }
    if (diff >= 3600) {
        parts.push(Math.floor(diff / 3600) + "h");
        diff %= 3600;
    }
    if (diff >= 60) {
        parts.push(Math.floor(diff / 60) + "m");
        diff %= 60;
    }
    parts.push(diff + "s");

    el.textContent = "Last scan: " + parts.join(" ") + " ago";
}

/* ------------------------------------------------------------
   3. Toast System (with batching for scan bursts)
   ------------------------------------------------------------ */

var _toastBatch = [];
var _toastBatchTimer = null;
var _toastBatchWindow = 1500; // ms to collect events before showing summary

function showToast(message, type) {
    _showToastImmediate(message, type);
}

function _showToastImmediate(message, type) {
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

// Queue a toast for batching. If multiple arrive within the batch window,
// they are collapsed into a single summary toast.
function queueBatchToast(message, type) {
    _toastBatch.push({message: message, type: type});
    if (_toastBatchTimer) clearTimeout(_toastBatchTimer);
    _toastBatchTimer = setTimeout(function () {
        _flushBatchToasts();
    }, _toastBatchWindow);
}

function _flushBatchToasts() {
    var batch = _toastBatch;
    _toastBatch = [];
    _toastBatchTimer = null;
    if (batch.length === 0) return;
    if (batch.length === 1) {
        _showToastImmediate(batch[0].message, batch[0].type);
        return;
    }
    // Summarise: count updates and queue additions separately.
    var updates = 0;
    var queued = 0;
    for (var i = 0; i < batch.length; i++) {
        var msg = batch[i].message.toLowerCase();
        if (msg.indexOf("update") !== -1 || msg.indexOf("available") !== -1) updates++;
        else if (msg.indexOf("queue") !== -1 || msg.indexOf("added") !== -1) queued++;
        else updates++; // fallback
    }
    var parts = [];
    if (updates > 0) parts.push(updates + " update" + (updates === 1 ? "" : "s") + " detected");
    if (queued > 0) parts.push(queued + " queued for approval");
    _showToastImmediate(parts.join(", "), "info");
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

function apiPost(url, body, successMsg, errorMsg, triggerEl, onSuccess) {
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
                if (onSuccess) onSuccess(result.data);
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

// Remove a queue row (and its accordion) from the DOM with animation.
function removeQueueRow(btn) {
    var row = btn ? btn.closest("tr") : null;
    if (!row) return;
    var accordion = row.nextElementSibling;
    var hasAccordion = accordion && accordion.classList.contains("accordion-panel");

    // Quick fade then remove — no height animation to avoid table layout shifts.
    row.style.transition = "opacity 0.15s ease";
    row.style.opacity = "0";
    if (hasAccordion) {
        accordion.style.transition = "opacity 0.15s ease";
        accordion.style.opacity = "0";
    }

    setTimeout(function () {
        if (hasAccordion) accordion.remove();
        row.remove();

        // Update "Pending Updates (N)" heading.
        var tbody = document.querySelector(".table-wrap tbody");
        var remaining = tbody ? tbody.querySelectorAll("tr.container-row").length : 0;
        var heading = document.querySelector(".card-header h2");
        if (heading) heading.textContent = "Pending Updates (" + remaining + ")";

        updateQueueBadge();

        // Show empty state if queue is now empty.
        if (remaining === 0 && tbody) {
            var emptyRow = document.createElement("tr");
            var td = document.createElement("td");
            td.setAttribute("colspan", "4");
            var wrapper = document.createElement("div");
            wrapper.className = "empty-state";
            var h3 = document.createElement("h3");
            h3.textContent = "No pending updates";
            var p1 = document.createElement("p");
            p1.textContent = "No containers are waiting for approval. Containers with a manual policy will appear here when updates are available.";
            var p2 = document.createElement("p");
            p2.style.marginTop = "var(--sp-2)";
            var link = document.createElement("a");
            link.href = "/settings";
            link.textContent = "Manage default policies";
            p2.appendChild(link);
            wrapper.appendChild(h3);
            wrapper.appendChild(p1);
            wrapper.appendChild(p2);
            td.appendChild(wrapper);
            emptyRow.appendChild(td);
            tbody.appendChild(emptyRow);
        }
    }, 180);
}

function approveUpdate(key, event) {
    var btn = event && event.target ? event.target.closest(".btn") : null;
    apiPost(
        "/api/approve/" + encodeURIComponent(key),
        null,
        "Approved update for " + key,
        "Failed to approve",
        btn,
        function () { removeQueueRow(btn); }
    );
}

function ignoreUpdate(key, event) {
    var btn = event && event.target ? event.target.closest(".btn") : null;
    apiPost(
        "/api/ignore/" + encodeURIComponent(key),
        null,
        "Version ignored for " + key,
        "Failed to ignore version",
        btn,
        function () { removeQueueRow(btn); }
    );
}

function rejectUpdate(key, event) {
    var btn = event && event.target ? event.target.closest(".btn") : null;
    apiPost(
        "/api/reject/" + encodeURIComponent(key),
        null,
        "Rejected update for " + key,
        "Failed to reject",
        btn,
        function () { removeQueueRow(btn); }
    );
}

// Bulk queue actions — fire each row's action with a staggered delay.
function bulkQueueAction(actionFn, triggerBtn) {
    var rows = document.querySelectorAll(".table-wrap tbody tr.container-row[data-queue-key]");
    if (!rows.length) return;
    if (triggerBtn) {
        triggerBtn.classList.add("loading");
        triggerBtn.disabled = true;
    }
    var delay = 0;
    var total = rows.length;
    var processed = 0;
    rows.forEach(function (row) {
        var key = row.getAttribute("data-queue-key");
        if (!key) { total--; return; }
        var btn = row.querySelector(".btn");
        setTimeout(function () {
            actionFn(key, { target: btn });
            processed++;
            if (processed >= total && triggerBtn) {
                triggerBtn.classList.remove("loading");
                triggerBtn.disabled = false;
            }
        }, delay);
        delay += 150;
    });
    // Handle edge case: no valid rows
    if (total <= 0 && triggerBtn) {
        triggerBtn.classList.remove("loading");
        triggerBtn.disabled = false;
    }
}

function approveAll(event) {
    var btn = event && event.target ? event.target.closest(".btn") : null;
    bulkQueueAction(approveUpdate, btn);
}

function ignoreAll(event) {
    var btn = event && event.target ? event.target.closest(".btn") : null;
    bulkQueueAction(ignoreUpdate, btn);
}

function rejectAll(event) {
    var btn = event && event.target ? event.target.closest(".btn") : null;
    bulkQueueAction(rejectUpdate, btn);
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

function switchToGHCR(name, ghcrImage) {
    if (!confirm("Switch " + name + " to " + ghcrImage + "?\n\nThis will recreate the container with the GHCR image. A snapshot will be taken first for rollback.")) {
        return;
    }
    var enc = encodeURIComponent(name);
    fetch("/api/containers/" + enc + "/switch-ghcr", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ target_image: ghcrImage })
    })
    .then(function(r) { return r.json(); })
    .then(function(data) {
        if (data.error) {
            showToast(data.error, "error");
        } else {
            showToast("Switching " + name + " to GHCR image...", "success");
        }
    })
    .catch(function() {
        showToast("Failed to switch to GHCR", "error");
    });
}

function updateToVersion(name) {
    var sel = document.getElementById("version-select");
    if (!sel || !sel.value) return;
    var tag = sel.value;
    showConfirm("Update to Version",
        "<p>Update <strong>" + name + "</strong> to <code>" + tag + "</code>?</p>"
    ).then(function(confirmed) {
        if (!confirmed) return;
        fetch("/api/containers/" + encodeURIComponent(name) + "/update-to-version", {
            method: "POST",
            headers: {"Content-Type": "application/json"},
            body: JSON.stringify({tag: tag})
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.error) { showToast(data.error, "error"); }
            else { showToast(data.message || "Update started", "success"); }
        })
        .catch(function() { showToast("Failed to trigger version update", "error"); });
    });
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
    window.location.href = "/container/" + encodeURIComponent(name);
}

/* ------------------------------------------------------------
   7. Stack Toggle
   ------------------------------------------------------------ */

function toggleStack(headerRow) {
    // Non-cluster path: stack is a <tbody class="stack-group">.
    var group = headerRow.closest(".stack-group");
    if (group) {
        group.classList.toggle("stack-collapsed");
        var expanded = !group.classList.contains("stack-collapsed");
        headerRow.setAttribute("aria-expanded", expanded ? "true" : "false");
        return;
    }

    // Cluster path: stack header is a <tr> inside a host-group <tbody>.
    // Toggle visibility of following sibling rows until the next stack-header
    // or host-header.
    var collapsed = headerRow.classList.toggle("stack-section-collapsed");
    var chevron = headerRow.querySelector(".stack-chevron");
    if (chevron) {
        chevron.style.transform = collapsed ? "rotate(0deg)" : "rotate(90deg)";
    }
    var sibling = headerRow.nextElementSibling;
    while (sibling && !sibling.classList.contains("stack-header") && !sibling.classList.contains("host-header")) {
        sibling.style.display = collapsed ? "none" : "";
        sibling = sibling.nextElementSibling;
    }
}

function toggleSwarmSection(row) {
    var section = row.closest(".swarm-section");
    if (!section) return;
    var isCollapsed = section.classList.toggle("swarm-collapsed");
    var icon = section.querySelector(".expand-icon");
    if (icon) icon.textContent = isCollapsed ? "\u25B8" : "\u25BE";
    // Hide/show all following svc-group tbody elements until the next non-svc-group.
    var sibling = section.nextElementSibling;
    while (sibling && sibling.classList.contains("svc-group")) {
        sibling.style.display = isCollapsed ? "none" : "";
        sibling = sibling.nextElementSibling;
    }
}

function toggleHostGroup(header) {
    var hostGroup = header.closest(".host-group");
    if (!hostGroup) return;
    var isCollapsed = hostGroup.classList.toggle("host-collapsed");
    var icon = header.querySelector(".expand-icon");
    if (icon) {
        icon.textContent = isCollapsed ? "\u25B8" : "\u25BE"; // right-pointing or down-pointing triangle
    }
}

function expandAllStacks() {
    var groups = document.querySelectorAll(".stack-group");
    for (var i = 0; i < groups.length; i++) {
        groups[i].classList.remove("stack-collapsed");
        var header = groups[i].querySelector(".stack-header");
        if (header) header.setAttribute("aria-expanded", "true");
    }
    // Expand stacks inside host groups (cluster path).
    var hostStackHeaders = document.querySelectorAll(".host-group .stack-header");
    for (var i = 0; i < hostStackHeaders.length; i++) {
        hostStackHeaders[i].classList.remove("stack-section-collapsed");
        var chevron = hostStackHeaders[i].querySelector(".stack-chevron");
        if (chevron) chevron.style.transform = "rotate(90deg)";
        var sibling = hostStackHeaders[i].nextElementSibling;
        while (sibling && !sibling.classList.contains("stack-header") && !sibling.classList.contains("host-header")) {
            sibling.style.display = "";
            sibling = sibling.nextElementSibling;
        }
    }
    // Expand host groups themselves.
    var hostGroups = document.querySelectorAll(".host-group");
    for (var i = 0; i < hostGroups.length; i++) {
        hostGroups[i].classList.remove("host-collapsed");
        var icon = hostGroups[i].querySelector(".expand-icon");
        if (icon) icon.textContent = "\u25BE";
    }
    // Also expand Swarm service groups.
    var svcGroups = document.querySelectorAll(".svc-group");
    for (var i = 0; i < svcGroups.length; i++) {
        svcGroups[i].classList.remove("svc-collapsed");
    }
}

function collapseAllStacks() {
    var groups = document.querySelectorAll(".stack-group");
    for (var i = 0; i < groups.length; i++) {
        groups[i].classList.add("stack-collapsed");
        var header = groups[i].querySelector(".stack-header");
        if (header) header.setAttribute("aria-expanded", "false");
    }
    // Collapse stacks inside host groups (cluster path).
    var hostStackHeaders = document.querySelectorAll(".host-group .stack-header");
    for (var i = 0; i < hostStackHeaders.length; i++) {
        hostStackHeaders[i].classList.add("stack-section-collapsed");
        var chevron = hostStackHeaders[i].querySelector(".stack-chevron");
        if (chevron) chevron.style.transform = "rotate(0deg)";
        var sibling = hostStackHeaders[i].nextElementSibling;
        while (sibling && !sibling.classList.contains("stack-header") && !sibling.classList.contains("host-header")) {
            sibling.style.display = "none";
            sibling = sibling.nextElementSibling;
        }
    }
    // Collapse host groups themselves.
    var hostGroups = document.querySelectorAll(".host-group");
    for (var i = 0; i < hostGroups.length; i++) {
        hostGroups[i].classList.add("host-collapsed");
        var icon = hostGroups[i].querySelector(".expand-icon");
        if (icon) icon.textContent = "\u25B8";
    }
    // Also collapse Swarm service groups.
    var svcGroups = document.querySelectorAll(".svc-group");
    for (var i = 0; i < svcGroups.length; i++) {
        svcGroups[i].classList.add("svc-collapsed");
    }
}

/* ------------------------------------------------------------
   7b. Swarm Service Toggle & Actions
   ------------------------------------------------------------ */

function toggleSvc(headerRow) {
    var group = headerRow.closest(".svc-group");
    if (!group) return;
    group.classList.toggle("svc-collapsed");
}

function triggerSvcUpdate(name, event) {
    var btn = event && event.target ? event.target.closest(".btn") : null;
    if (btn) {
        btn.classList.add("loading");
        btn.disabled = true;
        window._svcLoadingBtns = window._svcLoadingBtns || {};
        window._svcLoadingBtns[name] = btn;
        // Safety timeout — clear after 60s even if SSE never fires.
        setTimeout(function() {
            if (window._svcLoadingBtns && window._svcLoadingBtns[name]) {
                window._svcLoadingBtns[name].classList.remove("loading");
                window._svcLoadingBtns[name].disabled = false;
                delete window._svcLoadingBtns[name];
            }
        }, 60000);
    }
    apiPost(
        "/api/services/" + encodeURIComponent(name) + "/update",
        null,
        "Service update started for " + name,
        "Failed to trigger service update"
    );
    // Poll multiple times — Swarm updates take 10-30s to converge.
    var delays = [2000, 5000, 10000, 20000];
    for (var i = 0; i < delays.length; i++) {
        (function(d) {
            setTimeout(function() { refreshServiceRow(name); }, d);
        })(delays[i]);
    }
}

function changeSvcPolicy(name, newPolicy) {
    // Services use the same policy endpoint as containers.
    apiPost(
        "/api/containers/" + encodeURIComponent(name) + "/policy",
        { policy: newPolicy },
        "Policy changed to " + newPolicy + " for " + name,
        "Failed to change policy"
    );
}

function rollbackSvc(name, event) {
    var btn = event && event.target ? event.target.closest(".btn") : null;
    if (btn) {
        btn.classList.add("loading");
        btn.disabled = true;
        window._svcLoadingBtns = window._svcLoadingBtns || {};
        window._svcLoadingBtns["rb:" + name] = btn;
        setTimeout(function() {
            if (window._svcLoadingBtns && window._svcLoadingBtns["rb:" + name]) {
                window._svcLoadingBtns["rb:" + name].classList.remove("loading");
                window._svcLoadingBtns["rb:" + name].disabled = false;
                delete window._svcLoadingBtns["rb:" + name];
            }
        }, 60000);
    }
    apiPost(
        "/api/services/" + encodeURIComponent(name) + "/rollback",
        null,
        "Rollback started for " + name,
        "Failed to rollback " + name
    );
    // Poll multiple times — Swarm rollback takes 10-30s to converge.
    var delays = [2000, 5000, 10000, 20000];
    for (var i = 0; i < delays.length; i++) {
        (function(d) {
            setTimeout(function() { refreshServiceRow(name); }, d);
        })(delays[i]);
    }
}

// Cache of task data for scaled-to-0 services. Preserved across refreshes
// so task rows persistently show as "shutdown" instead of disappearing.
var _svcTaskCache = {};

function scaleSvc(name, replicas, wrap) {
    if (wrap) showBadgeSpinner(wrap);

    // Scaling to 0: cache current tasks, then mark rows as "shutdown".
    if (replicas === 0) {
        var group = document.querySelector('.svc-group[data-service="' + name + '"]');
        if (group) {
            // Capture task data from DOM before Swarm removes them.
            var taskRows = group.querySelectorAll(".svc-task-row");
            var cached = [];
            for (var t = 0; t < taskRows.length; t++) {
                var nodeCell = taskRows[t].querySelector(".svc-node");
                var tagCell = taskRows[t].querySelector(".mono");
                cached.push({
                    NodeText: nodeCell ? nodeCell.textContent : "",
                    Tag: tagCell ? tagCell.textContent : ""
                });
                // Immediately mark as shutdown.
                var stateCell = taskRows[t].querySelector(".badge");
                if (stateCell) {
                    stateCell.textContent = "shutdown";
                    stateCell.className = "badge badge-error";
                    stateCell.title = "";
                }
            }
            if (cached.length > 0) _svcTaskCache[name] = cached;
        }
    } else {
        // Scaling up: clear cache so refreshServiceRow uses live data.
        delete _svcTaskCache[name];
    }

    apiPost(
        "/api/services/" + encodeURIComponent(name) + "/scale",
        { replicas: replicas },
        "Scaled " + name + " to " + replicas + " replicas",
        "Failed to scale " + name
    );
    // Poll multiple times — Swarm scaling takes 5-15s to converge.
    var delays = [2000, 5000, 10000, 20000];
    for (var i = 0; i < delays.length; i++) {
        (function(d) {
            setTimeout(function() { refreshServiceRow(name); }, d);
        })(delays[i]);
    }
}

function refreshServiceRow(name) {
    fetch("/api/services/" + encodeURIComponent(name) + "/detail")
        .then(function(r) { return r.json(); })
        .then(function(svc) {
            var group = document.querySelector('.svc-group[data-service="' + name + '"]');
            if (!group) return;
            var header = group.querySelector(".svc-header");

            // Fully rebuild the scale badge wrap to ensure hover button is always present.
            var wrap = group.querySelector(".status-badge-wrap[data-service]");
            if (wrap) {
                wrap.style.pointerEvents = ""; // Clear spinner lock from showBadgeSpinner.
                var prevReplicas = svc.PrevReplicas || parseInt(wrap.getAttribute("data-prev-replicas"), 10) || 1;
                if (svc.DesiredReplicas > 0) {
                    var replicaClass = (svc.RunningReplicas === svc.DesiredReplicas) ? "svc-replicas-healthy" :
                        (svc.RunningReplicas > 0) ? "svc-replicas-degraded" : "svc-replicas-down";
                    wrap.setAttribute("data-prev-replicas", svc.DesiredReplicas);
                    wrap.innerHTML = '<span class="badge svc-replicas ' + replicaClass + ' badge-default">' +
                        escapeHtml(svc.Replicas || '') + '</span>' +
                        '<span class="badge badge-error badge-hover" onclick="event.stopPropagation(); scaleSvc(\'' +
                        escapeHtml(name) + '\', 0, this.closest(\'.status-badge-wrap\'))">Scale to 0</span>';
                } else {
                    wrap.setAttribute("data-prev-replicas", prevReplicas);
                    wrap.innerHTML = '<span class="badge svc-replicas svc-replicas-down badge-default">' +
                        escapeHtml(svc.Replicas || '0/0') + '</span>' +
                        '<span class="badge badge-success badge-hover" onclick="event.stopPropagation(); scaleSvc(\'' +
                        escapeHtml(name) + '\', ' + (prevReplicas > 0 ? prevReplicas : 1) + ', this.closest(\'.status-badge-wrap\'))">Scale up</span>';
                }
            }

            // Update image/version display in header.
            // All dynamic values are sanitised through escapeHtml before DOM insertion.
            // URLs come from our own API (server-computed), not user input.
            if (header) {
                var imgCell = header.querySelector(".cell-image");
                if (imgCell && svc.Tag) {
                    // Remove existing registry badge — applyRegistryBadges() will re-add it.
                    var oldBadge = imgCell.querySelector(".registry-badge");
                    if (oldBadge) oldBadge.remove();

                    var rvSpan = (svc.ResolvedVersion) ? ' <span class="resolved-ver">(' + escapeHtml(svc.ResolvedVersion) + ')</span>' : '';

                    if (svc.NewestVersion) {
                        var verHtml = escapeHtml(svc.NewestVersion);
                        if (svc.VersionURL && isSafeURL(svc.VersionURL)) {
                            verHtml = '<a href="' + escapeHtml(svc.VersionURL) + '" target="_blank" rel="noopener" class="version-new version-link">' + escapeHtml(svc.NewestVersion) + '</a>';
                        } else {
                            verHtml = '<span class="version-new">' + verHtml + '</span>';
                        }
                        imgCell.innerHTML = '<span class="version-current">' + escapeHtml(svc.Tag) + rvSpan + '</span>' +
                            ' <span class="version-arrow">&rarr;</span> ' + verHtml;
                    } else {
                        var tagHtml = escapeHtml(svc.Tag) + rvSpan;
                        if (svc.ChangelogURL && isSafeURL(svc.ChangelogURL)) {
                            imgCell.innerHTML = '<a href="' + escapeHtml(svc.ChangelogURL) + '" target="_blank" rel="noopener" class="version-link">' + tagHtml + '</a>';
                        } else {
                            imgCell.innerHTML = tagHtml;
                        }
                    }
                    imgCell.setAttribute("title", svc.Image || "");
                    applyRegistryBadges();
                }

                // Update has-update class.
                if (svc.HasUpdate) {
                    header.classList.add("has-update");
                } else {
                    header.classList.remove("has-update");
                }

                // Update action buttons (all dynamic values passed through escapeHtml).
                // But if the update is still in progress (loading btn tracked), keep the
                // spinner alive on the newly rendered button.
                var actionCell = header.querySelector("td:last-child .btn-group");
                if (actionCell) {
                    var isUpdating = window._svcLoadingBtns && window._svcLoadingBtns[name];
                    var isRolling  = window._svcLoadingBtns && window._svcLoadingBtns["rb:" + name];
                    var btns = "";
                    if (svc.HasUpdate && svc.Policy !== "pinned") {
                        btns += '<button class="btn btn-warning btn-sm' + (isUpdating ? ' loading' : '') + '"' +
                            (isUpdating ? ' disabled' : '') +
                            ' onclick="event.stopPropagation(); triggerSvcUpdate(\'' + escapeHtml(name) + '\', event)">Update</button>';
                    }
                    if (svc.UpdateStatus === "completed") {
                        btns += '<button class="btn btn-sm' + (isRolling ? ' loading' : '') + '"' +
                            (isRolling ? ' disabled' : '') +
                            ' onclick="event.stopPropagation(); rollbackSvc(\'' + escapeHtml(name) + '\', event)">Rollback</button>';
                    }
                    btns += '<a href="/service/' + encodeURIComponent(name) + '" class="btn btn-sm" onclick="event.stopPropagation()">Details</a>';
                    actionCell.innerHTML = btns;

                    // Update the tracked button references to the new DOM elements.
                    if (isUpdating) {
                        var newBtn = actionCell.querySelector(".btn-warning");
                        if (newBtn) window._svcLoadingBtns[name] = newBtn;
                    }
                    if (isRolling) {
                        var newRbBtn = actionCell.querySelector(".btn:not(.btn-warning)");
                        if (newRbBtn) window._svcLoadingBtns["rb:" + name] = newRbBtn;
                    }
                }
            }

            // Clear loading buttons only when update is truly done (no HasUpdate means
            // it succeeded, or UpdateStatus changed from what triggered the action).
            // The SSE "service update succeeded" event will fire refreshServiceRow again,
            // and at that point HasUpdate will be false — so the Update button won't be
            // rendered at all, naturally clearing the spinner.
            // We only force-clear here if the button is gone from the DOM entirely.
            if (window._svcLoadingBtns) {
                var keys = [name, "rb:" + name];
                for (var k = 0; k < keys.length; k++) {
                    var b = window._svcLoadingBtns[keys[k]];
                    if (b && !b.isConnected) {
                        // Button was removed from DOM (e.g. update completed, no longer shown).
                        delete window._svcLoadingBtns[keys[k]];
                    }
                }
            }

            // Update expanded task rows.
            var taskRows = group.querySelectorAll(".svc-task-row");
            // Remove existing task rows.
            for (var t = taskRows.length - 1; t >= 0; t--) {
                taskRows[t].remove();
            }
            // Rebuild task rows from fresh data.
            var taskHeader = group.querySelector(".svc-header");
            if (taskHeader && svc.Tasks && svc.Tasks.length > 0) {
                for (var t = 0; t < svc.Tasks.length; t++) {
                    var task = svc.Tasks[t];
                    var tr = document.createElement("tr");
                    tr.className = "svc-task-row";
                    var stateBadge;
                    if (task.State === "running") {
                        stateBadge = '<span class="badge badge-success">running</span>';
                    } else if (task.State === "preparing") {
                        stateBadge = '<span class="badge badge-info">preparing</span>';
                    } else {
                        stateBadge = '<span class="badge badge-error" title="' + escapeHtml(task.Error || '') + '">' + escapeHtml(task.State) + '</span>';
                    }
                    var nodeDisplay = escapeHtml(task.NodeName);
                    if (task.NodeAddr) {
                        nodeDisplay += ' <span class="svc-node-addr">(' + escapeHtml(task.NodeAddr) + ')</span>';
                    }
                    tr.innerHTML = '<td></td>' +
                        '<td class="svc-node">' + nodeDisplay + '</td>' +
                        '<td class="mono">' + escapeHtml(task.Tag || '') + '</td>' +
                        '<td></td>' +
                        '<td>' + stateBadge + '</td>' +
                        '<td></td>';
                    // Insert task rows after the header row.
                    taskHeader.parentNode.insertBefore(tr, taskHeader.nextSibling);
                }
            } else if (taskHeader && svc.DesiredReplicas === 0) {
                // Scaled to 0: show cached tasks as "shutdown" if available,
                // otherwise fall back to a placeholder.
                var cached = _svcTaskCache[name];
                if (cached && cached.length > 0) {
                    for (var t = cached.length - 1; t >= 0; t--) {
                        var tr = document.createElement("tr");
                        tr.className = "svc-task-row";
                        tr.innerHTML = '<td></td>' +
                            '<td class="svc-node">' + escapeHtml(cached[t].NodeText || '') + '</td>' +
                            '<td class="mono">' + escapeHtml(cached[t].Tag || '') + '</td>' +
                            '<td></td>' +
                            '<td><span class="badge badge-error">shutdown</span></td>' +
                            '<td></td>';
                        taskHeader.parentNode.insertBefore(tr, taskHeader.nextSibling);
                    }
                } else {
                    var tr = document.createElement("tr");
                    tr.className = "svc-task-row";
                    tr.innerHTML = '<td></td><td colspan="4" class="text-muted" style="padding:var(--sp-3)">Service scaled to 0 \u2014 no active tasks</td><td></td>';
                    taskHeader.parentNode.insertBefore(tr, taskHeader.nextSibling);
                }
            }

            group.classList.add("row-updated");
            setTimeout(function() { group.classList.remove("row-updated"); }, 300);
        })
        .catch(function() { /* ignore — falls back to defaults */ });
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

        // Deactivate exclusive siblings and reset their filter state.
        if (exclusive) {
            var siblings = bar.querySelectorAll('.filter-pill[data-exclusive="' + exclusive + '"]');
            for (var s = 0; s < siblings.length; s++) {
                siblings[s].classList.remove("active");
                var sibKey = siblings[s].getAttribute("data-filter");
                if (sibKey !== key) {
                    filterState[sibKey] = sibKey === "sort" ? "default" : "all";
                }
            }
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

// activateFilter is called from stat card clicks to set a filter + expand all stacks.
function activateFilter(key, value) {
    // Reset all filters first.
    filterState = { status: "all", updates: "all", sort: "default" };

    // Set the requested filter.
    filterState[key] = value;
    localStorage.setItem("sentinel-filters", JSON.stringify(filterState));

    // Update pill UI to match.
    var pills = document.querySelectorAll(".filter-pill");
    for (var i = 0; i < pills.length; i++) {
        var p = pills[i];
        if (p.getAttribute("data-filter") === key && p.getAttribute("data-value") === value) {
            p.classList.add("active");
        } else {
            p.classList.remove("active");
        }
    }

    expandAllStacks();
    applyFiltersAndSort();
}

function applyFiltersAndSort() {
    var table = document.getElementById("container-table");
    if (!table) return;

    // Scope filtering to the active tab; null means no tabs (process everything).
    var activeTab = activeDashboardTab;

    var stacks = table.querySelectorAll("tbody.stack-group");
    for (var s = 0; s < stacks.length; s++) {
        var stack = stacks[s];

        // Skip tbodies belonging to a different tab.
        var stackTab = stack.getAttribute("data-tab");
        if (activeTab !== null && activeTab !== "all" && stackTab !== null && stackTab !== activeTab) continue;

        // Skip tbodies hidden by tab switching.
        if (stack.style.display === "none") continue;

        var rows = stack.querySelectorAll(".container-row");
        var visibleCount = 0;
        for (var r = 0; r < rows.length; r++) {
            var row = rows[r];
            var show = true;
            if (filterState.status === "running") show = row.classList.contains("state-running");
            else if (filterState.status === "stopped") show = !row.classList.contains("state-running");
            if (show && filterState.updates === "pending") show = row.classList.contains("has-update");
            row.style.display = show ? "" : "none";
            if (show) visibleCount++;
        }
        stack.style.display = visibleCount === 0 ? "none" : "";
    }

    // Filter Swarm service groups too.
    var svcGroups = table.querySelectorAll("tbody.svc-group");
    for (var g = 0; g < svcGroups.length; g++) {
        var svcGroup = svcGroups[g];

        // Skip tbodies belonging to a different tab.
        var svcTab = svcGroup.getAttribute("data-tab");
        if (activeTab !== null && activeTab !== "all" && svcTab !== null && svcTab !== activeTab) continue;

        // Skip tbodies hidden by tab switching.
        if (svcGroup.style.display === "none") continue;

        var svcHeader = svcGroup.querySelector(".svc-header");
        if (!svcHeader) continue;
        var showSvc = true;
        if (filterState.status === "running") {
            var rb = svcHeader.querySelector(".svc-replicas");
            showSvc = rb && !rb.classList.contains("svc-replicas-down");
        } else if (filterState.status === "stopped") {
            var rb2 = svcHeader.querySelector(".svc-replicas");
            showSvc = rb2 && rb2.classList.contains("svc-replicas-down");
        }
        if (showSvc && filterState.updates === "pending") {
            showSvc = svcHeader.classList.contains("has-update");
        }
        svcGroup.style.display = showSvc ? "" : "none";
    }

    // Filter host-group tbodies (cluster mode).
    var hostGroups = table.querySelectorAll("tbody.host-group");
    for (var h = 0; h < hostGroups.length; h++) {
        var hg = hostGroups[h];

        var hgTab = hg.getAttribute("data-tab");
        if (activeTab !== null && activeTab !== "all" && hgTab !== null && hgTab !== activeTab) continue;
        if (hg.style.display === "none") continue;

        var hgRows = hg.querySelectorAll(".container-row");
        var hgVisible = 0;
        for (var hr = 0; hr < hgRows.length; hr++) {
            var hRow = hgRows[hr];
            var hShow = true;
            if (filterState.status === "running") hShow = hRow.classList.contains("state-running");
            else if (filterState.status === "stopped") hShow = !hRow.classList.contains("state-running");
            if (hShow && filterState.updates === "pending") hShow = hRow.classList.contains("has-update");
            hRow.style.display = hShow ? "" : "none";
            if (hShow) hgVisible++;
        }
    }

    if (filterState.sort !== "default") {
        sortRows(stacks);
        sortSwarmServices(table);
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
            tbody.appendChild(rows[i]);
        }
    }
}

function sortSwarmServices(table) {
    var groups = table.querySelectorAll("tbody.svc-group");
    if (groups.length < 2) return;
    var arr = [];
    for (var i = 0; i < groups.length; i++) {
        if (groups[i].style.display === "none") continue;
        arr.push(groups[i]);
    }
    if (filterState.sort === "alpha") {
        arr.sort(function(a, b) {
            return (a.getAttribute("data-service") || "").localeCompare(b.getAttribute("data-service") || "");
        });
    } else if (filterState.sort === "status") {
        arr.sort(function(a, b) {
            var sa = svcStatusScore(a), sb = svcStatusScore(b);
            return sa !== sb ? sb - sa : (a.getAttribute("data-service") || "").localeCompare(b.getAttribute("data-service") || "");
        });
    }
    for (var j = 0; j < arr.length; j++) {
        arr[j].parentNode.appendChild(arr[j]);
    }
}

function svcStatusScore(svcGroup) {
    var header = svcGroup.querySelector(".svc-header");
    if (!header) return 0;
    var rep = header.querySelector(".svc-replicas");
    if (rep && rep.classList.contains("svc-replicas-down")) return 3;
    if (header.classList.contains("has-update")) return 2;
    return 1;
}

function statusScore(row) {
    if (!row.classList.contains("state-running")) return 3;
    if (row.classList.contains("has-update")) return 2;
    return 1;
}

/* ------------------------------------------------------------
   9b. Dashboard Tabs
   ------------------------------------------------------------ */

var activeDashboardTab = null;

function initDashboardTabs() {
    var tabsEl = document.getElementById("dashboard-tabs");
    if (!tabsEl) return; // Standalone mode — no tabs

    var saved = localStorage.getItem("sentinel-dashboard-tab") || "all";

    // Validate saved tab exists; fall back to first tab button if not found.
    var buttons = tabsEl.querySelectorAll(".tab-btn");
    var found = false;
    for (var i = 0; i < buttons.length; i++) {
        if (buttons[i].getAttribute("data-tab") === saved) {
            found = true;
            break;
        }
    }
    if (!found && buttons.length > 0) {
        saved = buttons[0].getAttribute("data-tab");
    }

    switchDashboardTab(saved);

    for (var j = 0; j < buttons.length; j++) {
        buttons[j].addEventListener("click", function() {
            switchDashboardTab(this.getAttribute("data-tab"));
        });
    }
}

function switchDashboardTab(tabId) {
    var tabsEl = document.getElementById("dashboard-tabs");
    if (!tabsEl) return;

    activeDashboardTab = tabId;

    // Update tab button active states and aria-selected attributes.
    var buttons = tabsEl.querySelectorAll(".tab-btn");
    var activeBtn = null;
    for (var i = 0; i < buttons.length; i++) {
        var btn = buttons[i];
        if (btn.getAttribute("data-tab") === tabId) {
            btn.classList.add("active");
            btn.setAttribute("aria-selected", "true");
            activeBtn = btn;
        } else {
            btn.classList.remove("active");
            btn.setAttribute("aria-selected", "false");
        }
    }

    // Update stat cards from the active button's data attributes.
    if (activeBtn) {
        var total   = activeBtn.getAttribute("data-stats-total")   || "0";
        var running = activeBtn.getAttribute("data-stats-running") || "0";
        var pending = activeBtn.getAttribute("data-stats-pending") || "0";
        updateStats(parseInt(total, 10), parseInt(running, 10), parseInt(pending, 10));
    }

    // Show/hide tbody elements based on their data-tab attribute.
    var table = document.getElementById("container-table");
    if (table) {
        var tbodies = table.querySelectorAll("tbody");
        for (var t = 0; t < tbodies.length; t++) {
            var tb = tbodies[t];
            var tbTab = tb.getAttribute("data-tab");
            if (tbTab === null) {
                // No data-tab (e.g. thead-equivalent tbodies) — always visible.
                continue;
            }
            tb.style.display = (tabId === "all" || tbTab === tabId) ? "" : "none";
        }
    }

    localStorage.setItem("sentinel-dashboard-tab", tabId);

    applyFiltersAndSort();
}

function recalcTabStats() {
    var tabsEl = document.getElementById("dashboard-tabs");
    if (!tabsEl) return;

    var table = document.getElementById("container-table");
    if (!table) return;

    var buttons = tabsEl.querySelectorAll(".tab-btn");
    for (var i = 0; i < buttons.length; i++) {
        var btn = buttons[i];
        var tabId = btn.getAttribute("data-tab");

        var total = 0, running = 0, pending = 0;

        // Count stack-group container rows for this tab.
        var stacks = table.querySelectorAll('tbody.stack-group[data-tab="' + tabId + '"]');
        for (var s = 0; s < stacks.length; s++) {
            if (stacks[s].style.display === "none") continue;
            var rows = stacks[s].querySelectorAll(".container-row");
            for (var r = 0; r < rows.length; r++) {
                if (rows[r].style.display === "none") continue;
                total++;
                if (rows[r].classList.contains("state-running")) running++;
                if (rows[r].classList.contains("has-update")) pending++;
            }
        }

        // Count svc-group Swarm services for this tab.
        var svcGroups = table.querySelectorAll('tbody.svc-group[data-tab="' + tabId + '"]');
        for (var g = 0; g < svcGroups.length; g++) {
            if (svcGroups[g].style.display === "none") continue;
            total++;
            var svcHeader = svcGroups[g].querySelector(".svc-header");
            if (svcHeader) {
                var replicas = svcHeader.querySelector(".svc-replicas");
                if (replicas && replicas.classList.contains("svc-replicas-healthy")) running++;
                if (svcHeader.classList.contains("has-update")) pending++;
            }
        }

        // Count host-group container rows (cluster hosts) for this tab.
        var hostGroups = table.querySelectorAll('tbody.host-group[data-tab="' + tabId + '"]');
        for (var hg = 0; hg < hostGroups.length; hg++) {
            if (hostGroups[hg].style.display === "none") continue;
            var hgRows = hostGroups[hg].querySelectorAll(".container-row");
            for (var hr = 0; hr < hgRows.length; hr++) {
                if (hgRows[hr].style.display === "none") continue;
                total++;
                if (hgRows[hr].classList.contains("state-running")) running++;
                if (hgRows[hr].classList.contains("has-update")) pending++;
            }
        }

        btn.setAttribute("data-stats-total",   String(total));
        btn.setAttribute("data-stats-running",  String(running));
        btn.setAttribute("data-stats-pending",  String(pending));

        // Refresh displayed stat cards if this is the active tab.
        if (tabId === activeDashboardTab) {
            updateStats(total, running, pending);
        }

        // Update the badge text for non-"all" tabs immediately.
        if (tabId !== "all") {
            var badge = btn.querySelector(".tab-badge");
            if (badge) badge.textContent = String(total);
        }
    }

    // Update the "all" tab by summing all other tabs.
    var allBtn = tabsEl.querySelector('.tab-btn[data-tab="all"]');
    if (allBtn) {
        var allTotal = 0, allRunning = 0, allPending = 0;
        var nonAllBtns = tabsEl.querySelectorAll(".tab-btn:not([data-tab='all'])");
        for (var k = 0; k < nonAllBtns.length; k++) {
            allTotal += parseInt(nonAllBtns[k].getAttribute("data-stats-total") || "0", 10);
            allRunning += parseInt(nonAllBtns[k].getAttribute("data-stats-running") || "0", 10);
            allPending += parseInt(nonAllBtns[k].getAttribute("data-stats-pending") || "0", 10);
        }
        allBtn.setAttribute("data-stats-total", String(allTotal));
        allBtn.setAttribute("data-stats-running", String(allRunning));
        allBtn.setAttribute("data-stats-pending", String(allPending));
        var allBadge = allBtn.querySelector(".tab-badge");
        if (allBadge) allBadge.textContent = String(allTotal);
        if (activeDashboardTab === "all") {
            updateStats(allTotal, allRunning, allPending);
        }
    }
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

    // Enable/disable drag on stack groups
    var groups = table.querySelectorAll(".stack-group");
    for (var i = 0; i < groups.length; i++) {
        if (manageMode) {
            groups[i].setAttribute("draggable", "true");
        } else {
            groups[i].removeAttribute("draggable");
        }
    }
}

/* ------------------------------------------------------------
   Stack Drag Reordering
   ------------------------------------------------------------ */

(function () {
    var dragSrc = null;

    document.addEventListener("dragstart", function (e) {
        var group = e.target.closest(".stack-group");
        if (!group || !manageMode) return;
        // Only allow drag from the handle element.
        if (!e.target.closest(".stack-drag-handle")) {
            e.preventDefault();
            return;
        }
        dragSrc = group;
        group.classList.add("dragging");
        e.dataTransfer.effectAllowed = "move";
        e.dataTransfer.setData("text/plain", group.getAttribute("data-stack"));
    });

    document.addEventListener("dragover", function (e) {
        var group = e.target.closest(".stack-group");
        if (!group || !dragSrc || group === dragSrc) return;
        e.preventDefault();
        e.dataTransfer.dropEffect = "move";

        // Determine above/below by mouse Y relative to element midpoint
        var rect = group.getBoundingClientRect();
        var mid = rect.top + rect.height / 2;
        clearDragClasses(group);
        if (e.clientY < mid) {
            group.classList.add("drag-over-above");
        } else {
            group.classList.add("drag-over-below");
        }
    });

    document.addEventListener("dragleave", function (e) {
        var group = e.target.closest(".stack-group");
        if (group) clearDragClasses(group);
    });

    document.addEventListener("drop", function (e) {
        var group = e.target.closest(".stack-group");
        if (!group || !dragSrc || group === dragSrc) return;
        e.preventDefault();

        var rect = group.getBoundingClientRect();
        var mid = rect.top + rect.height / 2;
        var table = group.closest("table");

        if (e.clientY < mid) {
            table.insertBefore(dragSrc, group);
        } else {
            // Insert after: use nextElementSibling (next tbody)
            var next = group.nextElementSibling;
            if (next) {
                table.insertBefore(dragSrc, next);
            } else {
                table.appendChild(dragSrc);
            }
        }

        cleanupDrag();
        saveStackOrder();
    });

    document.addEventListener("dragend", function () {
        cleanupDrag();
    });

    function clearDragClasses(el) {
        el.classList.remove("drag-over-above", "drag-over-below");
    }

    function cleanupDrag() {
        if (dragSrc) {
            dragSrc.classList.remove("dragging");
            dragSrc = null;
        }
        var all = document.querySelectorAll(".drag-over-above, .drag-over-below");
        for (var i = 0; i < all.length; i++) {
            clearDragClasses(all[i]);
        }
    }

    function saveStackOrder() {
        var groups = document.querySelectorAll(".stack-group");
        var order = [];
        for (var i = 0; i < groups.length; i++) {
            var name = groups[i].getAttribute("data-stack");
            if (name) order.push(name);
        }
        fetch("/api/settings/stack-order", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ order: order })
        })
        .then(function (res) {
            if (!res.ok) throw new Error("save failed");
            showToast("Stack order saved", "success");
        })
        .catch(function () {
            showToast("Failed to save stack order", "error");
        });
    }
})();

/* ------------------------------------------------------------
   11. SSE Real-time Updates
   ------------------------------------------------------------ */

var sseReloadTimer = null;

function scheduleReload() {
    // Only full-reload on pages that render the container table (dashboard).
    // Other pages (settings, history, queue, etc.) don't need reloading.
    if (!document.getElementById("container-table")) return;
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

            if (oldRow) {
                var newRow = temp.querySelector(".container-row");

                if (newRow) {
                    oldRow.replaceWith(newRow);
                    newRow.classList.add("row-updated");
                }

                // Reapply checkbox state from selectedContainers after DOM patch.
                var newCb = newRow ? newRow.querySelector(".row-select") : null;
                if (newCb && selectedContainers[newCb.value]) {
                    newCb.checked = true;
                }
                recomputeSelectionState();
            }

            // Reapply badges and filters after DOM patch.
            applyRegistryBadges();
            applyFiltersAndSort();
            recalcTabStats();
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
    var newVals = [total, running, pending];
    for (var i = 0; i < values.length && i < newVals.length; i++) {
        if (values[i] && String(values[i].textContent).trim() !== String(newVals[i])) {
            values[i].textContent = newVals[i];
            values[i].classList.remove("stat-changed");
            // Force reflow to restart animation.
            void values[i].offsetWidth;
            values[i].classList.add("stat-changed");
        }
    }
    updatePendingColor(pending);
}

function refreshDashboardStats() {
    if (!document.getElementById("stats")) return;
    fetch("/api/stats", {credentials: "same-origin"})
        .then(function(r) { return r.json(); })
        .then(function(data) {
            updateStats(data.total, data.running, data.pending);
        })
        .catch(function() {});
}

function updatePendingColor(pending) {
    var stats = document.getElementById("stats");
    if (!stats) return;
    var pendingEl = stats.querySelectorAll(".stat-value")[2];
    if (!pendingEl) return;
    if (pending === 0 || pending === "0") {
        pendingEl.className = "stat-value success";
    } else {
        pendingEl.className = "stat-value warning";
    }
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
            // Batch toasts to avoid spamming during scans.
            queueBatchToast(data.message || ("Update: " + data.container_name), "info");
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
            // Batch toasts to avoid spamming during scans.
            queueBatchToast(data.message || "Queue updated", "info");
        } catch (_) {}
        // On the dashboard, container_update already patches rows individually.
        // Refresh stats and nav badge to keep counts in sync.
        if (document.getElementById("container-table")) {
            refreshDashboardStats();
            scheduleDigestBannerRefresh();
            updateQueueBadge();
        } else {
            // On other pages, just update the nav badge.
            updateQueueBadge();
        }
    });

    es.addEventListener("scan_complete", function () {
        // Clear scan button spinner.
        var scanBtn = document.getElementById("scan-btn");
        if (scanBtn) { scanBtn.classList.remove("loading"); scanBtn.disabled = false; }
        // Re-check pause state on scan events.
        checkPauseState();
        refreshLastScan();
        // On dashboard, rows are already patched by container_update events.
        // Refresh stats and banner to ensure counts are accurate.
        if (document.getElementById("container-table")) {
            refreshDashboardStats();
            recalcTabStats();
            scheduleDigestBannerRefresh();
        } else {
            scheduleReload();
        }
    });

    es.addEventListener("rate_limits", function (e) {
        try {
            var data = JSON.parse(e.data);
            var health = data.message || "ok";
            var el = document.getElementById("rate-limit-status");
            if (!el) return;
            var labels = { ok: "Healthy", low: "Needs Attention", exhausted: "Exhausted" };
            el.textContent = labels[health] || "Healthy";
            el.className = "stat-value";
            if (health === "ok") el.classList.add("success");
            else if (health === "low") el.classList.add("warning");
            else if (health === "exhausted") el.classList.add("error");
        } catch (_) {}
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

    es.addEventListener("digest_ready", function (e) {
        try {
            var data = JSON.parse(e.data);
            showToast(data.message || "Digest ready", "info");
        } catch (_) {}
        loadDigestBanner();
    });

    es.addEventListener("service_update", function (e) {
        try {
            var data = JSON.parse(e.data);
            queueBatchToast(data.message || ("Service: " + data.container_name), "info");
            if (data.container_name) {
                refreshServiceRow(data.container_name);
                // Swarm updates converge over 10-30s; do a second refresh.
                setTimeout(function() { refreshServiceRow(data.container_name); }, 10000);
            }
        } catch (_) {}
    });

    es.addEventListener("ghcr_check", function() {
        loadGHCRAlternatives();
    });

    es.onopen = function () {
        setConnectionStatus(true);
    };

    es.onerror = function () {
        setConnectionStatus(false);
    };
}

/* ------------------------------------------------------------
   11a. GHCR Alternatives
   ------------------------------------------------------------ */

function loadGHCRAlternatives() {
    fetch("/api/ghcr/alternatives")
        .then(function(r) { return r.json(); })
        .then(function(data) {
            ghcrAlternatives = {};
            if (data && data.length) {
                for (var i = 0; i < data.length; i++) {
                    var alt = data[i];
                    if (alt.available) {
                        ghcrAlternatives[alt.docker_hub_image] = alt;
                    }
                }
            }
            applyGHCRBadges();
        })
        .catch(function() { /* ignore — falls back to defaults */ });
}

var registryStyles = {
    "docker.io":        { label: "Hub",   cls: "registry-badge-hub" },
    "ghcr.io":          { label: "GHCR",  cls: "registry-badge-ghcr" },
    "lscr.io":          { label: "LSCR",  cls: "registry-badge-lscr" },
    "docker.gitea.com": { label: "Gitea", cls: "registry-badge-gitea" }
};

function applyRegistryBadges() {
    var rows = document.querySelectorAll("tr.container-row, tr.svc-header");
    rows.forEach(function(row) {
        var imageCell = row.querySelector(".cell-image");
        if (!imageCell) return;

        // Skip if already badged.
        if (imageCell.querySelector(".registry-badge")) return;

        var reg = row.getAttribute("data-registry") || "docker.io";
        var style = registryStyles[reg] || registryStyles["docker.io"];

        var badge = document.createElement("span");
        badge.className = "registry-badge " + style.cls;
        badge.textContent = style.label;
        imageCell.insertBefore(badge, imageCell.firstChild);
    });
}

function applyGHCRBadges() {
    var rows = document.querySelectorAll("tr.container-row");
    rows.forEach(function(row) {
        // Remove existing GHCR badges first (avoid duplicates on refresh).
        var existing = row.querySelector(".ghcr-badge");
        if (existing) existing.remove();

        var imageCell = row.querySelector(".cell-image");
        if (!imageCell) return;

        var title = imageCell.getAttribute("title") || "";
        // Only for docker.io images (no dots in first segment, or explicit docker.io prefix).
        // Skip library/* (official images like nginx, redis).
        var repo = parseDockerRepo(title);
        if (!repo || !ghcrAlternatives[repo]) return;

        var badge = document.createElement("span");
        badge.className = "ghcr-badge";
        badge.textContent = "GHCR";
        badge.title = "Also available on GitHub Container Registry";
        imageCell.appendChild(badge);
    });
}

function parseDockerRepo(imageRef) {
    // Strip tag/digest
    var ref = imageRef.split("@")[0].split(":")[0];
    // Remove explicit docker.io/ prefix
    ref = ref.replace(/^docker\.io\//, "");
    // Skip non-docker.io registries (contain dots in first segment)
    var firstSegment = ref.split("/")[0];
    if (firstSegment.indexOf(".") !== -1) return null;
    // Skip official library images (single segment like "nginx")
    if (ref.indexOf("/") === -1) return null;
    // Skip library/ prefix images
    if (ref.indexOf("library/") === 0) return null;
    return ref;
}

function renderGHCRAlternatives() {
    var container = document.getElementById("ghcr-alternatives-table");
    if (!container) return;

    fetch("/api/ghcr/alternatives")
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (!data || data.length === 0) {
                while (container.firstChild) container.removeChild(container.firstChild);
                var emptyP = document.createElement("p");
                emptyP.className = "text-muted";
                emptyP.textContent = "No GHCR alternatives detected yet. Alternatives are checked after each scan.";
                container.appendChild(emptyP);
                return;
            }

            var table = document.createElement("table");
            table.className = "table-readonly";
            var thead = document.createElement("thead");
            var headRow = document.createElement("tr");
            ["Docker Hub Image", "GHCR Equivalent", "Status"].forEach(function(label) {
                var th = document.createElement("th");
                th.textContent = label;
                headRow.appendChild(th);
            });
            thead.appendChild(headRow);
            table.appendChild(thead);

            var tbody = document.createElement("tbody");
            data.forEach(function(alt) {
                var tr = document.createElement("tr");

                var tdHub = document.createElement("td");
                tdHub.className = "mono";
                tdHub.textContent = alt.docker_hub_image + ":" + alt.tag;
                tr.appendChild(tdHub);

                var tdGHCR = document.createElement("td");
                tdGHCR.className = "mono";
                tdGHCR.textContent = alt.available ? alt.ghcr_image + ":" + alt.tag : "\u2014";
                tr.appendChild(tdGHCR);

                var tdStatus = document.createElement("td");
                var statusBadge = document.createElement("span");
                if (!alt.available) {
                    statusBadge.className = "badge badge-muted";
                    statusBadge.textContent = "Not available";
                } else if (alt.digest_match) {
                    statusBadge.className = "badge badge-success";
                    statusBadge.textContent = "Identical";
                } else {
                    statusBadge.className = "badge badge-warning";
                    statusBadge.textContent = "Different build";
                }
                tdStatus.appendChild(statusBadge);
                tr.appendChild(tdStatus);

                tbody.appendChild(tr);
            });
            table.appendChild(tbody);

            while (container.firstChild) container.removeChild(container.firstChild);
            container.appendChild(table);
        })
        .catch(function() {
            while (container.firstChild) container.removeChild(container.firstChild);
            var errP = document.createElement("p");
            errP.className = "text-muted";
            errP.textContent = "Failed to load GHCR alternatives.";
            container.appendChild(errP);
        });
}

/* ------------------------------------------------------------
   11b. Digest Banner
   ------------------------------------------------------------ */

function loadDigestBanner() {
    var banner = document.getElementById("digest-banner");
    if (!banner) return;

    fetch("/api/digest/banner", {credentials: "same-origin"})
        .then(function (res) { return res.json(); })
        .then(function (data) {
            if (data.count > 0) {
                var text = "Pending updates: " + data.pending.join(", ") +
                    " (" + data.count + " container" + (data.count === 1 ? "" : "s") + " awaiting action)";
                document.getElementById("digest-banner-text").textContent = text;
                banner.style.display = "";
                banner.classList.remove("banner-hidden");
            } else {
                banner.classList.add("banner-hidden");
            }
        })
        .catch(function () { banner.classList.add("banner-hidden"); });
}

var _digestBannerTimer = null;

// Debounced digest banner refresh — avoids flickering when multiple events fire.
function scheduleDigestBannerRefresh() {
    if (_digestBannerTimer) clearTimeout(_digestBannerTimer);
    _digestBannerTimer = setTimeout(function () {
        _digestBannerTimer = null;
        loadDigestBanner();
    }, 1500);
}

// Update the queue badge count in the nav without a full page reload.
function updateQueueBadge() {
    fetch("/api/queue", {credentials: "same-origin"})
        .then(function (r) { return r.json(); })
        .then(function (data) {
            var badges = document.querySelectorAll(".nav-badge");
            var count = Array.isArray(data) ? data.length : 0;
            for (var i = 0; i < badges.length; i++) {
                var link = badges[i].closest("a");
                if (link && link.href && link.href.indexOf("/queue") !== -1) {
                    badges[i].textContent = count;
                    badges[i].style.display = count > 0 ? "" : "none";
                }
            }
        })
        .catch(function () {});
}

function dismissDigestBanner() {
    var banner = document.getElementById("digest-banner");
    if (banner) banner.classList.add("banner-hidden");

    fetch("/api/digest/banner/dismiss", {
        method: "POST",
        credentials: "same-origin",
        headers: {"Content-Type": "application/json"},
        body: "{}"
    });
}

/* ------------------------------------------------------------
   12. Initialisation
   ------------------------------------------------------------ */

document.addEventListener("DOMContentLoaded", function () {
    initTheme();
    // Only init SSE on authenticated pages (skip login/setup).
    var path = window.location.pathname;
    if (path !== "/login" && path !== "/setup") {
        initSSE();
    }
    initPauseBanner();
    loadFooterVersion();
    loadDigestBanner();
    initFilters();
    initDashboardTabs();
    refreshLastScan();

    // Apply stack default preference.
    var stackPref = localStorage.getItem("sentinel-stacks") || "collapsed";
    if (stackPref === "expanded") {
        expandAllStacks();
    }

    initSettingsPage();
    initAccordionPersistence();

    // Color the pending stat card based on initial value.
    var stats = document.getElementById("stats");
    if (stats) {
        var pendingEl = stats.querySelectorAll(".stat-value")[2];
        if (pendingEl) {
            var val = parseInt(pendingEl.textContent.trim(), 10);
            updatePendingColor(val);
        }
    }

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

        // Apply registry source badges and load GHCR alternatives.
        applyRegistryBadges();
        loadGHCRAlternatives();
    }

    // Stop/Start/Restart badge click delegation (containers only — service scale uses inline onclick).
    document.addEventListener("click", function(e) {
        var badge = e.target.closest(".status-badge-wrap .badge-hover");
        if (!badge) return;
        e.stopPropagation();
        var wrap = badge.closest(".status-badge-wrap");
        if (!wrap) return;

        // Container stop/start/restart actions.
        var name = wrap.getAttribute("data-name");
        if (!name) return;
        var action = wrap.getAttribute("data-action") || "restart";

        // Show inline spinner immediately.
        showBadgeSpinner(wrap);

        var hostId = wrap.getAttribute("data-host-id");
        var endpoint = "/api/containers/" + encodeURIComponent(name) + "/" + action;
        if (hostId) endpoint += "?host=" + encodeURIComponent(hostId);
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

/* ------------------------------------------------------------
   14. Digest Settings
   ------------------------------------------------------------ */

var NOTIFY_MODE_LABELS = {
    "default": "Immediate + summary",
    "every_scan": "Every scan",
    "digest_only": "Summary only",
    "muted": "Silent"
};

function modeUsesDigest(mode) {
    return mode === "default" || mode === "digest_only";
}

function updateDigestScheduleVisibility(mode) {
    var section = document.getElementById("digest-schedule-section");
    if (section) section.style.display = modeUsesDigest(mode) ? "" : "none";
}

function updateNotifyModePreview(mode) {
    var preview = document.getElementById("notify-mode-preview");
    if (preview) preview.textContent = NOTIFY_MODE_LABELS[mode] || mode;
}

function onNotifyModeChange(mode) {
    updateDigestScheduleVisibility(mode);
    updateNotifyModePreview(mode);
    saveDigestSettings();
}

function getSelectedNotifyMode() {
    var radios = document.querySelectorAll('input[name="default-notify-mode"]');
    for (var i = 0; i < radios.length; i++) {
        if (radios[i].checked) return radios[i].value;
    }
    return "default";
}

function loadDigestSettings() {
    fetch("/api/settings/digest", {credentials: "same-origin"})
        .then(function (res) { return res.json(); })
        .then(function (data) {
            var mode = data.default_notify_mode || "default";
            var radios = document.querySelectorAll('input[name="default-notify-mode"]');
            for (var i = 0; i < radios.length; i++) {
                radios[i].checked = (radios[i].value === mode);
            }
            updateDigestScheduleVisibility(mode);
            updateNotifyModePreview(mode);
            var el = document.getElementById("digest-time");
            if (el) el.value = data.digest_time || "09:00";
            el = document.getElementById("digest-interval");
            if (el) el.value = data.digest_interval || "24h";
        })
        .catch(function () {});
}

function saveDigestSettings() {
    var mode = getSelectedNotifyMode();
    var body = {
        default_notify_mode: mode,
        digest_enabled: modeUsesDigest(mode)
    };
    var el = document.getElementById("digest-time");
    if (el && el.value) body.digest_time = el.value;
    el = document.getElementById("digest-interval");
    if (el && el.value) body.digest_interval = el.value;

    fetch("/api/settings/digest", {
        method: "POST",
        credentials: "same-origin",
        headers: {"Content-Type": "application/json"},
        body: JSON.stringify(body)
    })
    .then(function (res) { return res.json(); })
    .then(function (data) {
        if (data.status === "ok") showToast("Settings saved", "success");
    })
    .catch(function () { showToast("Failed to save settings", "error"); });
}

function triggerDigest() {
    fetch("/api/digest/trigger", {
        method: "POST",
        credentials: "same-origin",
        headers: {"Content-Type": "application/json"}
    })
    .then(function (res) { return res.json(); })
    .then(function (data) {
        showToast(data.message || "Digest triggered", "info");
    })
    .catch(function () { showToast("Failed to trigger digest", "error"); });
}

function loadContainerNotifyPrefs() {
    var container = document.getElementById("container-notify-prefs");
    if (!container) return;

    fetch("/api/settings/container-notify-prefs", {credentials: "same-origin"})
        .then(function (res) { return res.json(); })
        .then(function (prefs) {
            fetch("/api/containers", {credentials: "same-origin"})
                .then(function (res) { return res.json(); })
                .then(function (containers) {
                    renderContainerNotifyPrefs(container, containers, prefs);
                });
        })
        .catch(function () {
            while (container.firstChild) container.removeChild(container.firstChild);
            var msg = document.createElement("p");
            msg.className = "text-muted";
            msg.textContent = "Failed to load preferences";
            container.appendChild(msg);
        });
}

function renderContainerNotifyPrefs(el, containers, prefs) {
    while (el.firstChild) el.removeChild(el.firstChild);

    if (!containers || containers.length === 0) {
        var msg = document.createElement("p");
        msg.className = "text-muted";
        msg.textContent = "No containers found";
        el.appendChild(msg);
        return;
    }

    var allModes = [
        {value: "default", label: "Immediate + summary"},
        {value: "every_scan", label: "Every scan"},
        {value: "digest_only", label: "Summary only"},
        {value: "muted", label: "Silent"}
    ];

    // Build data with stack info
    var items = [];
    var overrideCount = 0;
    for (var i = 0; i < containers.length; i++) {
        var mode = (prefs[containers[i].name] && prefs[containers[i].name].mode) || "default";
        if (mode !== "default") overrideCount++;
        items.push({name: containers[i].name, mode: mode, stack: containers[i].stack || ""});
    }

    // Group by stack, sorted: named stacks alphabetically, standalone last
    var stackMap = {};
    var stackOrder = [];
    for (var s = 0; s < items.length; s++) {
        var key = items[s].stack;
        if (!stackMap[key]) {
            stackMap[key] = [];
            stackOrder.push(key);
        }
        stackMap[key].push(items[s]);
    }
    stackOrder.sort(function(a, b) {
        if (a === "") return 1;
        if (b === "") return -1;
        return a.localeCompare(b);
    });
    // Sort containers within each stack
    for (var sk in stackMap) {
        stackMap[sk].sort(function(a, b) { return a.name.localeCompare(b.name); });
    }

    // Summary
    var summary = document.createElement("p");
    summary.className = "notify-prefs-summary";
    summary.textContent = overrideCount === 0
        ? "All " + items.length + " containers use the default notification mode. Select containers below to set a different mode."
        : overrideCount + " of " + items.length + " containers have custom settings. Select containers to change their mode.";
    el.appendChild(summary);

    // Toolbar
    var toolbar = document.createElement("div");
    toolbar.className = "notify-prefs-toolbar";

    var selectAllBtn = document.createElement("button");
    selectAllBtn.className = "btn";
    selectAllBtn.textContent = "Select all";
    selectAllBtn.addEventListener("click", function() { toggleAllPrefs(true); });
    toolbar.appendChild(selectAllBtn);

    var deselectBtn = document.createElement("button");
    deselectBtn.className = "btn";
    deselectBtn.textContent = "Deselect all";
    deselectBtn.addEventListener("click", function() { toggleAllPrefs(false); });
    toolbar.appendChild(deselectBtn);

    el.appendChild(toolbar);

    // Container list grouped by stack
    var listWrap = document.createElement("div");
    listWrap.id = "notify-prefs-list";

    for (var g = 0; g < stackOrder.length; g++) {
        var stackName = stackOrder[g];
        var groupItems = stackMap[stackName];

        // Card wrapper for this stack
        var groupCard = document.createElement("div");
        groupCard.className = "notify-prefs-group";

        var heading = document.createElement("div");
        heading.className = "notify-prefs-group-heading";
        heading.textContent = stackName === "swarm" ? "Swarm Services" : (stackName || "Standalone");
        groupCard.appendChild(heading);

        // Grid for this stack
        var grid = document.createElement("div");
        grid.className = "notify-prefs-list";

        for (var j = 0; j < groupItems.length; j++) {
            var item = groupItems[j];
            var label = document.createElement("label");
            label.className = "notify-pref-item" + (item.mode !== "default" ? " has-override" : "");
            label.dataset.name = item.name;

            var cb = document.createElement("input");
            cb.type = "checkbox";
            cb.value = item.name;
            cb.addEventListener("change", function() {
                this.closest(".notify-pref-item").classList.toggle("checked", this.checked);
                updatePrefsActionBar();
            });
            label.appendChild(cb);

            var nameSpan = document.createElement("span");
            nameSpan.className = "notify-pref-name";
            nameSpan.textContent = item.name;
            label.appendChild(nameSpan);

            if (item.mode !== "default") {
                var badge = document.createElement("span");
                badge.className = "notify-pref-badge";
                badge.textContent = NOTIFY_MODE_LABELS[item.mode] || item.mode;
                label.appendChild(badge);
            }

            grid.appendChild(label);
        }
        groupCard.appendChild(grid);
        listWrap.appendChild(groupCard);
    }
    el.appendChild(listWrap);

    // Action bar (hidden until selection)
    var actionBar = document.createElement("div");
    actionBar.className = "notify-prefs-action-bar";
    actionBar.id = "notify-prefs-action-bar";
    actionBar.style.display = "none";

    var countSpan = document.createElement("span");
    countSpan.className = "action-count";
    countSpan.id = "notify-prefs-action-count";
    countSpan.textContent = "0 selected";
    actionBar.appendChild(countSpan);

    var actionSel = document.createElement("select");
    actionSel.className = "setting-select";
    actionSel.id = "notify-prefs-action-mode";
    for (var k = 0; k < allModes.length; k++) {
        var opt = document.createElement("option");
        opt.value = allModes[k].value;
        opt.textContent = allModes[k].label;
        actionSel.appendChild(opt);
    }
    actionBar.appendChild(actionSel);

    var applyBtn = document.createElement("button");
    applyBtn.className = "btn btn-success";
    applyBtn.textContent = "Apply";
    applyBtn.addEventListener("click", function() { applyPrefsToSelected(); });
    actionBar.appendChild(applyBtn);

    var resetBtn = document.createElement("button");
    resetBtn.className = "btn";
    resetBtn.textContent = "Reset to default";
    resetBtn.addEventListener("click", function() { applyPrefsToSelected("default"); });
    actionBar.appendChild(resetBtn);

    el.appendChild(actionBar);
}

function toggleAllPrefs(checked) {
    var cbs = document.querySelectorAll("#notify-prefs-list input[type=checkbox]");
    for (var i = 0; i < cbs.length; i++) {
        cbs[i].checked = checked;
        cbs[i].closest(".notify-pref-item").classList.toggle("checked", checked);
    }
    updatePrefsActionBar();
}

function updatePrefsActionBar() {
    var cbs = document.querySelectorAll("#notify-prefs-list input[type=checkbox]:checked");
    var bar = document.getElementById("notify-prefs-action-bar");
    var count = document.getElementById("notify-prefs-action-count");
    if (!bar) return;
    bar.style.display = cbs.length > 0 ? "" : "none";
    if (count) count.textContent = cbs.length + " selected";
}

function applyPrefsToSelected(forceMode) {
    var modeSel = document.getElementById("notify-prefs-action-mode");
    var mode = forceMode || (modeSel ? modeSel.value : "default");
    var cbs = document.querySelectorAll("#notify-prefs-list input[type=checkbox]:checked");
    if (cbs.length === 0) return;

    var label = NOTIFY_MODE_LABELS[mode] || mode;
    var action = forceMode ? "Reset" : "Set";
    if (!confirm(action + " " + cbs.length + " container" + (cbs.length > 1 ? "s" : "") + " to \"" + label + "\"?")) return;

    var pending = cbs.length;
    for (var i = 0; i < cbs.length; i++) {
        (function(name) {
            fetch("/api/containers/" + encodeURIComponent(name) + "/notify-pref", {
                method: "POST",
                credentials: "same-origin",
                headers: {"Content-Type": "application/json"},
                body: JSON.stringify({mode: mode})
            })
            .then(function() {
                pending--;
                if (pending === 0) {
                    showToast(action + " " + cbs.length + " containers to " + label, "success");
                    loadContainerNotifyPrefs();
                }
            })
            .catch(function() { pending--; });
        })(cbs[i].value);
    }
}

function setContainerNotifyPref(name, mode) {
    fetch("/api/containers/" + encodeURIComponent(name) + "/notify-pref", {
        method: "POST",
        credentials: "same-origin",
        headers: {"Content-Type": "application/json"},
        body: JSON.stringify({mode: mode})
    })
    .then(function (res) { return res.json(); })
    .then(function (data) {
        if (data.status === "ok") {
            showToast("Notification mode updated for " + name, "success");
            loadContainerNotifyPrefs();
        }
    })
    .catch(function () { showToast("Failed to update notification mode", "error"); });
}


/* ------------------------------------------------------------
   14. HTML Escape Helper
   ------------------------------------------------------------ */

function escapeHtml(str) {
    if (!str) return "";
    return str
        .replace(/&/g, "&amp;")
        .replace(/</g, "&lt;")
        .replace(/>/g, "&gt;")
        .replace(/"/g, "&quot;")
        .replace(/'/g, "&#039;");
}

// isSafeURL validates that a URL string starts with http:// or https://.
// Used as a defence-in-depth check before inserting server-provided URLs
// into href attributes via innerHTML.
function isSafeURL(url) {
    return typeof url === "string" && (url.indexOf("https://") === 0 || url.indexOf("http://") === 0);
}


/* ------------------------------------------------------------
   15. Registry Credentials & Rate Limits
   ------------------------------------------------------------ */

var registryData = {};
var registryCredentials = [];

function loadRegistries() {
    fetch("/api/settings/registries")
        .then(function(r) { return r.json(); })
        .then(function(data) {
            registryData = data;
            registryCredentials = [];
            Object.keys(data).forEach(function(reg) {
                if (data[reg].credential) {
                    registryCredentials.push(data[reg].credential);
                }
            });
            renderRegistryStatus();
            renderRegistryCredentials();
        })
        .catch(function(err) { console.error("Failed to load registries:", err); });
}

function renderRegistryStatus() {
    var container = document.getElementById("registry-status-table");
    var warningsEl = document.getElementById("registry-warnings");
    if (!container) return;

    var registries = Object.keys(registryData);
    if (registries.length === 0) {
        container.textContent = "";
        var emptyDiv = document.createElement("div");
        emptyDiv.className = "empty-state";
        emptyDiv.style.padding = "var(--sp-6) var(--sp-4)";
        var emptyH3 = document.createElement("h3");
        emptyH3.textContent = "No registries detected";
        var emptyP = document.createElement("p");
        emptyP.textContent = "Registries will appear here after the first scan.";
        emptyDiv.appendChild(emptyH3);
        emptyDiv.appendChild(emptyP);
        container.appendChild(emptyDiv);
        if (warningsEl) warningsEl.textContent = "";
        return;
    }

    // Build the table using DOM methods for safety.
    var table = document.createElement("table");
    table.className = "data-table";
    var thead = document.createElement("thead");
    var headRow = document.createElement("tr");
    ["Registry", "Containers", "Used", "Resets", "Auth"].forEach(function(label) {
        var th = document.createElement("th");
        th.textContent = label;
        headRow.appendChild(th);
    });
    thead.appendChild(headRow);
    table.appendChild(thead);

    var tbody = document.createElement("tbody");
    var warnings = [];
    registries.sort();

    registries.forEach(function(reg) {
        var info = registryData[reg];
        var rl = info.rate_limit;
        var cred = info.credential;

        var images = rl ? rl.container_count : 0;
        var usedText = "\u2014";
        var usedClass = "";
        var resets = "\u2014";

        if (rl && rl.has_limits) {
            var used = rl.limit - rl.remaining;
            usedText = used + " / " + rl.limit;
            if (rl.remaining <= 0) {
                usedClass = "text-error";
            } else if (rl.limit > 0 && used / rl.limit >= 0.8) {
                usedClass = "text-warning";
            }
            if (rl.reset_at && rl.reset_at !== "0001-01-01T00:00:00Z") {
                var resetTime = new Date(rl.reset_at);
                var now = new Date();
                var diffMs = resetTime - now;
                if (diffMs > 0) {
                    var hours = Math.floor(diffMs / 3600000);
                    var mins = Math.floor((diffMs % 3600000) / 60000);
                    resets = hours + "h " + mins + "m";
                } else {
                    resets = "expired";
                }
            }
        } else if (rl && !rl.has_limits && rl.limit === -1 && rl.last_updated === "0001-01-01T00:00:00Z") {
            usedText = "\u2014";
        } else if (rl && !rl.has_limits) {
            usedText = "No limits";
        }

        var tr = document.createElement("tr");

        var tdReg = document.createElement("td");
        var regStrong = document.createElement("strong");
        regStrong.textContent = reg;
        tdReg.appendChild(regStrong);
        tr.appendChild(tdReg);

        var tdImages = document.createElement("td");
        tdImages.textContent = images;
        tr.appendChild(tdImages);

        var tdUsed = document.createElement("td");
        if (usedClass) {
            var usedSpan = document.createElement("span");
            usedSpan.className = usedClass;
            usedSpan.textContent = usedText;
            tdUsed.appendChild(usedSpan);
        } else {
            tdUsed.textContent = usedText;
        }
        tr.appendChild(tdUsed);

        var tdResets = document.createElement("td");
        tdResets.textContent = resets;
        tr.appendChild(tdResets);

        var tdAuth = document.createElement("td");
        var authBadge = document.createElement("span");
        if (cred) {
            authBadge.className = "badge badge-success";
            authBadge.textContent = "\u2713 Yes";
        } else {
            authBadge.className = "badge badge-muted";
            authBadge.textContent = "\u2717 None";
        }
        tdAuth.appendChild(authBadge);
        tr.appendChild(tdAuth);

        tbody.appendChild(tr);

        if (rl && rl.has_limits && !cred) {
            warnings.push(reg);
        }
    });

    table.appendChild(tbody);
    container.textContent = "";
    container.appendChild(table);

    if (warningsEl) {
        warningsEl.textContent = "";
        warnings.forEach(function(reg) {
            var alertDiv = document.createElement("div");
            alertDiv.className = "alert alert-warning";
            alertDiv.textContent = "\u26a0 " + reg + ": No credentials. Unauthenticated rate limits apply.";
            warningsEl.appendChild(alertDiv);
        });
    }
}

function renderRegistryCredentials() {
    var container = document.getElementById("credential-list");
    if (!container) return;
    container.textContent = "";

    if (registryCredentials.length === 0) {
        var emptyDiv = document.createElement("div");
        emptyDiv.className = "empty-state";
        emptyDiv.style.padding = "var(--sp-6) var(--sp-4)";
        var emptyH3 = document.createElement("h3");
        emptyH3.textContent = "No credentials configured";
        var emptyP = document.createElement("p");
        emptyP.textContent = "Add credentials for registries that enforce rate limits (e.g. Docker Hub).";
        emptyDiv.appendChild(emptyH3);
        emptyDiv.appendChild(emptyP);
        container.appendChild(emptyDiv);
        // Don't return — dropdown population code below must still run.
    }

    registryCredentials.forEach(function(cred, index) {
        var card = document.createElement("div");
        card.className = "channel-card";
        card.setAttribute("data-index", index);

        // Header
        var header = document.createElement("div");
        header.className = "channel-card-header";
        var typeBadge = document.createElement("span");
        typeBadge.className = "channel-type-badge";
        typeBadge.textContent = cred.registry;
        header.appendChild(typeBadge);

        var actions = document.createElement("div");
        actions.className = "channel-actions";
        var testBtn = document.createElement("button");
        testBtn.className = "btn btn-sm";
        testBtn.textContent = "Test";
        testBtn.setAttribute("data-index", index);
        testBtn.addEventListener("click", function() { testRegistryCredential(parseInt(this.getAttribute("data-index"), 10)); });
        actions.appendChild(testBtn);
        var delBtn = document.createElement("button");
        delBtn.className = "btn btn-sm btn-error";
        delBtn.textContent = "Remove";
        delBtn.setAttribute("data-index", index);
        delBtn.addEventListener("click", function() { deleteRegistryCredential(parseInt(this.getAttribute("data-index"), 10)); });
        actions.appendChild(delBtn);
        header.appendChild(actions);
        card.appendChild(header);

        // Hidden field to preserve registry value on collect.
        var regHidden = document.createElement("input");
        regHidden.type = "hidden";
        regHidden.setAttribute("data-field", "registry");
        regHidden.value = cred.registry;
        card.appendChild(regHidden);

        // Fields: username and password only (registry is fixed via badge).
        var fields = document.createElement("div");
        fields.className = "channel-fields";
        var textFieldDefs = [
            { label: "Username", field: "username", type: "text", value: cred.username },
            { label: "Password / Token", field: "secret", type: "password", value: cred.secret }
        ];
        textFieldDefs.forEach(function(def) {
            var fieldDiv = document.createElement("div");
            fieldDiv.className = "channel-field";
            var labelSpan = document.createElement("span");
            labelSpan.className = "channel-field-label";
            labelSpan.textContent = def.label;
            fieldDiv.appendChild(labelSpan);
            var input = document.createElement("input");
            input.type = def.type;
            input.className = "channel-field-input";
            input.value = def.value || "";
            input.setAttribute("data-field", def.field);
            fieldDiv.appendChild(input);
            fields.appendChild(fieldDiv);
        });
        card.appendChild(fields);

        // Registry-specific help callout.
        var hints = {
            "docker.io": "Create a Personal Access Token at hub.docker.com \u2192 Account Settings \u2192 Personal access tokens. Read-only scope is sufficient. Authenticated users get 200 pulls/6h (vs 100 anonymous).",
            "ghcr.io": "Create a Personal Access Token at github.com \u2192 Settings \u2192 Developer settings \u2192 Personal access tokens (classic). Select the read:packages scope.",
            "lscr.io": "LinuxServer images are also available via GHCR (ghcr.io/linuxserver/*). Use your LinuxServer Fleet credentials, or switch the registry to ghcr.io for simpler auth.",
            "docker.gitea.com": "Use your Gitea account credentials. Gitea registries typically have no rate limits."
        };
        var hintText = hints[cred.registry];
        if (hintText) {
            var helpDiv = document.createElement("div");
            helpDiv.className = "alert alert-info";
            helpDiv.style.margin = "var(--sp-3) var(--sp-4) var(--sp-4)";
            var helpIcon = document.createElement("span");
            helpIcon.className = "alert-info-icon";
            helpIcon.textContent = "\u2139";
            helpDiv.appendChild(helpIcon);
            var helpSpan = document.createElement("span");
            helpSpan.textContent = hintText;
            helpDiv.appendChild(helpSpan);
            card.appendChild(helpDiv);
        }

        container.appendChild(card);
    });

    // Populate the "Add" dropdown with registries that don't already have credentials.
    var addSelect = document.getElementById("registry-type-select");
    if (addSelect) {
        while (addSelect.options.length > 1) addSelect.remove(1);
        var usedRegs = {};
        registryCredentials.forEach(function(c) { usedRegs[c.registry] = true; });
        var allRegs = Object.keys(registryData).sort();
        // Always include common registries even if not yet detected.
        ["docker.io", "ghcr.io", "lscr.io", "docker.gitea.com"].forEach(function(r) {
            if (allRegs.indexOf(r) === -1) allRegs.push(r);
        });
        allRegs.sort();
        allRegs.forEach(function(reg) {
            if (!usedRegs[reg]) {
                var opt = document.createElement("option");
                opt.value = reg;
                opt.textContent = reg;
                addSelect.appendChild(opt);
            }
        });
    }
}

function addRegistryCredential() {
    var select = document.getElementById("registry-type-select");
    if (!select) return;
    if (!select.value) {
        showToast("Select a registry from the dropdown first", "warning");
        select.focus();
        return;
    }
    var reg = select.value;
    var id = "reg-" + Date.now() + "-" + Math.random().toString(36).substr(2, 9);
    registryCredentials.push({ id: id, registry: reg, username: "", secret: "" });
    select.value = "";
    renderRegistryCredentials();
    showToast("Added " + reg + " \u2014 enter credentials and save", "info");
}

function deleteRegistryCredential(index) {
    registryCredentials.splice(index, 1);
    renderRegistryCredentials();
    showToast("Credential removed \u2014 save to persist", "info");
}

function collectRegistryCredentialsFromDOM() {
    var cards = document.querySelectorAll("#credential-list .channel-card");
    cards.forEach(function(card, i) {
        if (i < registryCredentials.length) {
            var inputs = card.querySelectorAll("[data-field]");
            inputs.forEach(function(input) {
                var field = input.getAttribute("data-field");
                if (field) registryCredentials[i][field] = input.value;
            });
        }
    });
}

function saveRegistryCredentials(event) {
    collectRegistryCredentialsFromDOM();
    var btn = event && event.target ? event.target.closest(".btn") : null;
    if (btn) btn.classList.add("loading");

    fetch("/api/settings/registries", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(registryCredentials)
    })
    .then(function(r) { return r.json(); })
    .then(function(data) {
        if (btn) btn.classList.remove("loading");
        if (data.error) {
            showToast("Failed: " + data.error, "error");
        } else {
            showToast("Registry credentials saved", "success");
            loadRegistries();
            // Probe runs server-side in background; re-fetch after it completes.
            setTimeout(function() { loadRegistries(); }, 3000);
        }
    })
    .catch(function(err) {
        if (btn) btn.classList.remove("loading");
        showToast("Save failed: " + err, "error");
    });
}

function testRegistryCredential(index) {
    collectRegistryCredentialsFromDOM();
    var cred = registryCredentials[index];
    if (!cred) return;

    fetch("/api/settings/registries/test", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ id: cred.id, registry: cred.registry, username: cred.username, secret: cred.secret })
    })
    .then(function(r) { return r.json(); })
    .then(function(data) {
        if (data.success) {
            showToast("Credentials valid for " + cred.registry, "success");
            // Refresh rate-limit status after background probe, but don't
            // reload credentials — unsaved entries would be wiped.
            setTimeout(function() {
                fetch("/api/settings/registries")
                    .then(function(r) { return r.json(); })
                    .then(function(d) {
                        registryData = d;
                        renderRegistryStatus();
                    });
            }, 3000);
        } else {
            showToast("Test failed: " + (data.error || "unknown error"), "error");
        }
    })
    .catch(function(err) { showToast("Test failed: " + err, "error"); });
}


/* ------------------------------------------------------------
   16. Rate Limit Status — Dashboard Polling
   ------------------------------------------------------------ */

function updateRateLimitStatus() {
    fetch("/api/ratelimits")
        .then(function(r) { return r.json(); })
        .then(function(data) {
            var el = document.getElementById("rate-limit-status");
            if (!el) return;
            var health = data.health || "ok";
            var labels = { ok: "Healthy", low: "Needs Attention", exhausted: "Exhausted" };
            el.textContent = labels[health] || "Healthy";
            el.className = "stat-value";
            if (health === "ok") el.classList.add("success");
            else if (health === "low") el.classList.add("warning");
            else if (health === "exhausted") el.classList.add("error");
        })
        .catch(function() { /* ignore — falls back to defaults */ });
}

// Fetch on initial load; live updates arrive via SSE rate_limits event.
if (document.getElementById("rate-limit-status")) {
    updateRateLimitStatus();
}

// --- About tab ---
// All dynamic values are sanitised through escapeHtml() before insertion.

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
