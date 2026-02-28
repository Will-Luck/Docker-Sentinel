/* ============================================================
   2. Settings Page
   2a. Settings Helpers
   ============================================================ */

import { showToast } from "./utils.js";

/* ------------------------------------------------------------
   2. Settings Page — initSettingsPage
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
        if (window.applyTheme) window.applyTheme(themeSelect.value);
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

            // Dry-run toggle.
            var dryRunToggle = document.getElementById("dry-run-toggle");
            if (dryRunToggle) {
                var dryRun = settings["dry_run"] === "true";
                dryRunToggle.checked = dryRun;
                updateToggleText("dry-run-text", dryRun);
            }

            // Pull-only toggle.
            var pullOnlyToggle = document.getElementById("pull-only-toggle");
            if (pullOnlyToggle) {
                var pullOnly = settings["pull_only"] === "true";
                pullOnlyToggle.checked = pullOnly;
                updateToggleText("pull-only-text", pullOnly);
            }

            // Update delay.
            var updateDelayInput = document.getElementById("update-delay");
            if (updateDelayInput) {
                updateDelayInput.value = settings["update_delay"] || "";
            }

            // Maintenance window.
            var maintenanceWindowInput = document.getElementById("maintenance-window");
            if (maintenanceWindowInput) {
                maintenanceWindowInput.value = settings["maintenance_window"] || "";
            }

            // Compose sync toggle.
            var composeSyncToggle = document.getElementById("compose-sync-toggle");
            if (composeSyncToggle) {
                var composeSync = settings["compose_sync"] === "true";
                composeSyncToggle.checked = composeSync;
                updateToggleText("compose-sync-text", composeSync);
            }

            // Image backup toggle.
            var imageBackupToggle = document.getElementById("image-backup-toggle");
            if (imageBackupToggle) {
                var imageBackup = settings["image_backup"] === "true";
                imageBackupToggle.checked = imageBackup;
                updateToggleText("image-backup-text", imageBackup);
            }

            // Show stopped toggle.
            var showStoppedToggle = document.getElementById("show-stopped-toggle");
            if (showStoppedToggle) {
                var showStopped = settings["show_stopped"] === "true";
                showStoppedToggle.checked = showStopped;
                updateToggleText("show-stopped-text", showStopped);
            }

            // Remove volumes toggle.
            var removeVolumesToggle = document.getElementById("remove-volumes-toggle");
            if (removeVolumesToggle) {
                var removeVolumes = settings["remove_volumes"] === "true";
                removeVolumesToggle.checked = removeVolumes;
                updateToggleText("remove-volumes-text", removeVolumes);
            }

            // Scan concurrency input.
            var scanConcInput = document.getElementById("scan-concurrency-input");
            if (scanConcInput && settings["scan_concurrency"]) {
                var sc = parseInt(settings["scan_concurrency"], 10);
                if (!isNaN(sc) && sc >= 1) { scanConcInput.value = sc; }
            }

            // HA discovery toggle.
            var haToggle = document.getElementById("ha-discovery-toggle");
            if (haToggle) {
                var haEnabled = settings["ha_discovery_enabled"] === "true";
                haToggle.checked = haEnabled;
                updateToggleText("ha-discovery-text", haEnabled);
            }
            var haPrefixInput = document.getElementById("ha-discovery-prefix");
            if (haPrefixInput) {
                haPrefixInput.value = settings["ha_discovery_prefix"] || "homeassistant";
            }

            updateScanPreviews();
        })
        .catch(function() { /* ignore -- falls back to defaults */ });

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

    // Auto-select Security tab when auth is off (no user button visible).
    var securityBtn = document.querySelector('[data-tab="security"]');
    if (securityBtn && !document.querySelector('.nav-user-btn')) {
        securityBtn.click();
    }

    // Load notification channels.
    if (window.loadNotificationChannels) window.loadNotificationChannels();

    // Load digest settings, per-container notification preferences, and templates.
    if (window.loadDigestSettings) window.loadDigestSettings();
    if (window.loadContainerNotifyPrefs) window.loadContainerNotifyPrefs();
    if (window.loadNotifyTemplates) window.loadNotifyTemplates();

    // Load registry credentials and rate limits.
    if (window.loadRegistries) window.loadRegistries();

    // Load release note sources.
    if (window.loadReleaseSources) window.loadReleaseSources();

    // Load GHCR alternatives table on registries tab.
    if (window.renderGHCRAlternatives) window.renderGHCRAlternatives();

    // Load About info.
    if (window.loadAboutInfo) window.loadAboutInfo();

    // Load cluster settings.
    if (window.loadClusterSettings) window.loadClusterSettings();

    // Load webhook settings.
    loadWebhookSettings();
}

// Local helper — clearAccordionState is also in dashboard.js but duplicated
// here to avoid circular import (settings page doesn't need full dashboard).
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

/* ------------------------------------------------------------
   2a. Settings Helpers
   ------------------------------------------------------------ */

function normaliseDuration(dur) {
    return dur
        .replace("0m0s", "").replace("0s", "")
        .replace(/^0h/, "");
}

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

function parseDuration(dur) {
    var match = dur.match(/^(\d+)(s|m|h)$/);
    if (!match) return null;
    return { value: match[1], unit: match[2] };
}

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

function updateScanPreviews() {
    // Scan Schedule preview
    var el = document.getElementById("scan-schedule-preview");
    if (el) {
        var pauseToggle = document.getElementById("pause-toggle");
        var cronInput = document.getElementById("cron-schedule");
        var pollSelect = document.getElementById("poll-interval");
        if (pauseToggle && pauseToggle.checked) {
            el.textContent = "Paused";
        } else if (cronInput && cronInput.value) {
            el.textContent = "Cron: " + cronInput.value;
        } else if (pollSelect) {
            if (pollSelect.value === "custom") {
                var cv = (document.getElementById("poll-custom-value") || {}).value || "";
                var cu = (document.getElementById("poll-custom-unit") || {}).value || "";
                el.textContent = cv && cu ? "Every " + cv + cu : "Custom";
            } else {
                el.textContent = "Every " + pollSelect.options[pollSelect.selectedIndex].text;
            }
        }
    }
    // Update Policy preview
    var el2 = document.getElementById("update-policy-preview");
    if (el2) {
        var policySelect = document.getElementById("default-policy");
        var txt = "Default: " + (policySelect ? policySelect.value : "manual");
        var mwInput = document.getElementById("maintenance-window");
        if (mwInput && mwInput.value) {
            txt += ", window " + mwInput.value;
        } else {
            var graceSelect = document.getElementById("grace-period");
            if (graceSelect) {
                if (graceSelect.value === "custom") {
                    var gv = (document.getElementById("grace-custom-value") || {}).value || "";
                    var gu = (document.getElementById("grace-custom-unit") || {}).value || "";
                    txt += gv && gu ? ", grace " + gv + gu : "";
                } else {
                    txt += ", grace " + graceSelect.options[graceSelect.selectedIndex].text;
                }
            }
        }
        el2.textContent = txt;
    }
}

function onPollIntervalChange(value) {
    var wrap = document.getElementById("poll-custom-wrap");
    if (value === "custom") {
        if (wrap) wrap.style.display = "";
        updateScanPreviews();
        return;
    }
    if (wrap) wrap.style.display = "none";
    setPollInterval(value);
    updateScanPreviews();
}

function applyCustomPollInterval() {
    var unit = document.getElementById("poll-custom-unit");
    var val = document.getElementById("poll-custom-value");
    if (!unit || !val || !unit.value || !val.value) return;
    setPollInterval(val.value + unit.value);
    updateScanPreviews();
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
            showToast("Network error -- could not update poll interval", "error");
        });
}

function setDefaultPolicy(value) {
    updateScanPreviews();
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
            showToast("Network error -- could not update default policy", "error");
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
            showToast("Network error -- could not update rollback policy", "error");
        });
}

function onGracePeriodChange(value) {
    var wrap = document.getElementById("grace-custom-wrap");
    if (value === "custom") {
        if (wrap) wrap.style.display = "";
        updateScanPreviews();
        return;
    }
    if (wrap) wrap.style.display = "none";
    setGracePeriod(value);
    updateScanPreviews();
}

function applyCustomGracePeriod() {
    var unit = document.getElementById("grace-custom-unit");
    var val = document.getElementById("grace-custom-value");
    if (!unit || !val || !unit.value || !val.value) return;
    setGracePeriod(val.value + unit.value);
    updateScanPreviews();
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
            showToast("Network error -- could not update grace period", "error");
        });
}

function setPauseState(paused) {
    updatePauseToggleText(paused);
    updateScanPreviews();
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
            showToast("Network error -- could not update pause state", "error");
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
            showToast("Network error -- could not update setting", "error");
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
            showToast("Network error -- could not save filters", "error");
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
    fetch("/api/settings/image-cleanup", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ enabled: enabled }) })
        .then(function(r) { return r.json(); })
        .then(function(data) { showToast(data.message || "Setting updated", "success"); })
        .catch(function() { showToast("Network error -- could not update setting", "error"); });
}

function saveCronSchedule() {
    var input = document.getElementById("cron-schedule");
    if (!input) return;
    updateScanPreviews();
    fetch("/api/settings/schedule", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ schedule: input.value }) })
        .then(function(resp) { return resp.json().then(function(data) { return { ok: resp.ok, data: data }; }); })
        .then(function(result) {
            if (result.ok) { showToast(result.data.message || "Schedule updated", "success"); }
            else { showToast(result.data.error || "Failed to update schedule", "error"); }
        })
        .catch(function() { showToast("Network error -- could not update schedule", "error"); });
}

function setDependencyAware(enabled) {
    updateToggleText("dep-aware-text", enabled);
    fetch("/api/settings/dependency-aware", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ enabled: enabled }) })
        .then(function(r) { return r.json(); })
        .then(function(data) { showToast(data.message || "Setting updated", "success"); })
        .catch(function() { showToast("Network error -- could not update setting", "error"); });
}

function setHooksEnabled(enabled) {
    updateToggleText("hooks-toggle-text", enabled);
    fetch("/api/settings/hooks-enabled", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ enabled: enabled }) })
        .then(function(r) { return r.json(); })
        .then(function(data) { showToast(data.message || "Setting updated", "success"); })
        .catch(function() { showToast("Network error -- could not update setting", "error"); });
}

function setHooksWriteLabels(enabled) {
    updateToggleText("hooks-labels-text", enabled);
    fetch("/api/settings/hooks-write-labels", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ enabled: enabled }) })
        .then(function(r) { return r.json(); })
        .then(function(data) { showToast(data.message || "Setting updated", "success"); })
        .catch(function() { showToast("Network error -- could not update setting", "error"); });
}

function setDryRun(enabled) {
    updateToggleText("dry-run-text", enabled);
    fetch("/api/settings/dry-run", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ enabled: enabled }) })
        .then(function(r) { return r.json(); })
        .then(function(data) { showToast(data.message || "Setting updated", "success"); })
        .catch(function() { showToast("Network error -- could not update setting", "error"); });
}

function setPullOnly(enabled) {
    updateToggleText("pull-only-text", enabled);
    fetch("/api/settings/pull-only", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ enabled: enabled }) })
        .then(function(r) { return r.json(); })
        .then(function(data) { showToast(data.message || "Setting updated", "success"); })
        .catch(function() { showToast("Network error -- could not update setting", "error"); });
}

function setComposeSync(enabled) {
    updateToggleText("compose-sync-text", enabled);
    fetch("/api/settings/compose-sync", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ enabled: enabled }) })
        .then(function(r) { return r.json(); })
        .then(function(data) { showToast(data.message || "Setting updated", "success"); })
        .catch(function() { showToast("Network error -- could not update setting", "error"); });
}

function setImageBackup(enabled) {
    updateToggleText("image-backup-text", enabled);
    fetch("/api/settings/image-backup", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ enabled: enabled }) })
        .then(function(r) { return r.json(); })
        .then(function(data) { showToast(data.message || "Setting updated", "success"); })
        .catch(function() { showToast("Network error -- could not update setting", "error"); });
}

function setShowStopped(enabled) {
    updateToggleText("show-stopped-text", enabled);
    fetch("/api/settings/show-stopped", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ enabled: enabled }) })
        .then(function(r) { return r.json(); })
        .then(function(data) { showToast(data.message || "Setting updated", "success"); })
        .catch(function() { showToast("Network error -- could not update setting", "error"); });
}

function setRemoveVolumes(enabled) {
    updateToggleText("remove-volumes-text", enabled);
    fetch("/api/settings/remove-volumes", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ enabled: enabled }) })
        .then(function(r) { return r.json(); })
        .then(function(data) { showToast(data.message || "Setting updated", "success"); })
        .catch(function() { showToast("Network error -- could not update setting", "error"); });
}

function setScanConcurrency() {
    var input = document.getElementById("scan-concurrency-input");
    var n = parseInt(input ? input.value : "1", 10);
    if (isNaN(n) || n < 1 || n > 20) {
        showToast("Concurrency must be between 1 and 20", "error");
        return;
    }
    fetch("/api/settings/scan-concurrency", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ concurrency: n }) })
        .then(function(r) { return r.json(); })
        .then(function(data) { showToast(data.message || "Setting updated", "success"); })
        .catch(function() { showToast("Network error -- could not update setting", "error"); });
}

function setHADiscovery(enabled) {
    updateToggleText("ha-discovery-text", enabled);
    var prefix = (document.getElementById("ha-discovery-prefix") || {}).value || "";
    fetch("/api/settings/ha-discovery", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ enabled: enabled, prefix: prefix }) })
        .then(function(r) {
            if (r.status === 204) { showToast("HA discovery " + (enabled ? "enabled" : "disabled"), "success"); return; }
            return r.json().then(function(d) { if (d.error) showToast(d.error, "error"); });
        })
        .catch(function() { showToast("Network error -- could not update setting", "error"); });
}

function saveHADiscoveryPrefix() {
    var prefix = (document.getElementById("ha-discovery-prefix") || {}).value || "";
    var enabled = (document.getElementById("ha-discovery-toggle") || {}).checked || false;
    fetch("/api/settings/ha-discovery", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ enabled: enabled, prefix: prefix }) })
        .then(function(r) {
            if (r.status === 204) { showToast("Discovery prefix saved", "success"); return; }
            return r.json().then(function(d) { if (d.error) showToast(d.error, "error"); });
        })
        .catch(function() { showToast("Network error -- could not save prefix", "error"); });
}

function setUpdateDelay() {
    var input = document.getElementById("update-delay");
    if (!input) return;
    updateScanPreviews();
    fetch("/api/settings/update-delay", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ duration: input.value }) })
        .then(function(resp) { return resp.json().then(function(data) { return { ok: resp.ok, data: data }; }); })
        .then(function(result) {
            if (result.ok) { showToast(result.data.message || "Update delay saved", "success"); }
            else { showToast(result.data.error || "Failed to save update delay", "error"); }
        })
        .catch(function() { showToast("Network error -- could not save update delay", "error"); });
}

function toggleCollapsible(headerEl) {
    var expanded = headerEl.getAttribute("aria-expanded") === "true";
    headerEl.setAttribute("aria-expanded", expanded ? "false" : "true");
    var body = headerEl.nextElementSibling;
    if (body) {
        body.style.display = expanded ? "none" : "";
    }
}

function saveDockerTLS() {
    var ca = (document.getElementById("docker-tls-ca") || {}).value || "";
    var cert = (document.getElementById("docker-tls-cert") || {}).value || "";
    var key = (document.getElementById("docker-tls-key") || {}).value || "";

    fetch("/api/settings/docker-tls", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ca: ca, cert: cert, key: key })
    })
        .then(function(resp) {
            return resp.json().then(function(data) {
                return { ok: resp.ok, data: data };
            });
        })
        .then(function(result) {
            if (result.ok) {
                showToast(result.data.message || "Docker TLS settings saved", "success");
                var banner = document.getElementById("general-restart-banner");
                if (banner) banner.style.display = "block";
            } else {
                showToast(result.data.error || "Failed to save Docker TLS settings", "error");
            }
        })
        .catch(function() {
            showToast("Network error -- could not save Docker TLS settings", "error");
        });
}

function testDockerTLS() {
    var ca = (document.getElementById("docker-tls-ca") || {}).value || "";
    var cert = (document.getElementById("docker-tls-cert") || {}).value || "";
    var key = (document.getElementById("docker-tls-key") || {}).value || "";

    if (!ca || !cert || !key) {
        showToast("All three certificate paths are required for testing", "error");
        return;
    }

    var btn = document.getElementById("docker-tls-test-btn");
    if (btn) btn.disabled = true;

    fetch("/api/settings/docker-tls-test", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ca: ca, cert: cert, key: key })
    })
        .then(function(resp) { return resp.json(); })
        .then(function(data) {
            if (data.success) {
                showToast("Docker TLS connection successful", "success");
            } else {
                showToast("Connection failed: " + (data.error || "unknown error"), "error");
            }
        })
        .catch(function() {
            showToast("Network error -- could not test Docker TLS connection", "error");
        })
        .finally(function() {
            if (btn) btn.disabled = false;
        });
}

/* ------------------------------------------------------------
   Webhook Settings
   ------------------------------------------------------------ */

function loadWebhookSettings() {
    var toggle = document.getElementById("webhook-enabled-toggle");
    if (!toggle) return;

    fetch("/api/settings/webhook-info")
        .then(function(r) { return r.json(); })
        .then(function(data) {
            var enabled = data.enabled === "true";
            toggle.checked = enabled;
            updateToggleText("webhook-enabled-text", enabled);
            showWebhookConfig(enabled, data.secret || "");
        })
        .catch(function() { /* ignore -- falls back to defaults */ });
}

function showWebhookConfig(enabled, secret) {
    var configDiv = document.getElementById("webhook-config");
    if (!configDiv) return;
    configDiv.style.display = enabled ? "" : "none";

    var urlInput = document.getElementById("webhook-url");
    if (urlInput) {
        urlInput.value = window.location.origin + "/api/webhook";
    }

    var secretInput = document.getElementById("webhook-secret");
    if (secretInput) {
        secretInput.value = secret;
        // If the secret is masked (contains ****), show a hint.
        var hint = document.getElementById("webhook-secret-hint");
        if (hint) {
            hint.style.display = (secret && secret.indexOf("****") !== -1) ? "" : "none";
        }
    }
}

function setWebhookEnabled(enabled) {
    updateToggleText("webhook-enabled-text", enabled);
    fetch("/api/settings/webhook-enabled", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled: enabled })
    })
        .then(function(resp) {
            return resp.json().then(function(data) {
                return { ok: resp.ok, data: data };
            });
        })
        .then(function(result) {
            if (result.ok) {
                showToast(result.data.message || "Setting updated", "success");
                // Reload webhook info to get the auto-generated secret.
                loadWebhookSettings();
            } else {
                showToast(result.data.error || "Failed to update setting", "error");
            }
        })
        .catch(function() {
            showToast("Network error -- could not update setting", "error");
        });
}

function regenerateWebhookSecret() {
    if (!confirm("This will invalidate all existing webhook integrations. Continue?")) return;

    fetch("/api/settings/webhook-secret", {
        method: "POST",
        headers: { "Content-Type": "application/json" }
    })
        .then(function(resp) {
            return resp.json().then(function(data) {
                return { ok: resp.ok, data: data };
            });
        })
        .then(function(result) {
            if (result.ok) {
                var secretInput = document.getElementById("webhook-secret");
                if (secretInput) secretInput.value = result.data.secret || "";
                // Hide the masked hint — the full secret is now visible.
                var hint = document.getElementById("webhook-secret-hint");
                if (hint) hint.style.display = "none";
                showToast("Webhook secret regenerated — copy it now, it won't be shown again", "success");
            } else {
                showToast(result.data.error || "Failed to regenerate secret", "error");
            }
        })
        .catch(function() {
            showToast("Network error -- could not regenerate secret", "error");
        });
}

function copyWebhookURL() {
    var input = document.getElementById("webhook-url");
    if (!input || !input.value) return;
    navigator.clipboard.writeText(input.value).then(function() {
        showToast("Webhook URL copied", "success");
    }).catch(function() {
        input.select();
        document.execCommand("copy");
        showToast("Webhook URL copied", "success");
    });
}

function copyWebhookSecret() {
    var input = document.getElementById("webhook-secret");
    if (!input || !input.value) return;
    navigator.clipboard.writeText(input.value).then(function() {
        showToast("Webhook secret copied", "success");
    }).catch(function() {
        input.select();
        document.execCommand("copy");
        showToast("Webhook secret copied", "success");
    });
}

/* ------------------------------------------------------------
   Maintenance Window
   ------------------------------------------------------------ */

function saveMaintenanceWindow() {
    var input = document.getElementById("maintenance-window");
    if (!input) return;
    updateScanPreviews();
    fetch("/api/settings/maintenance-window", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ value: input.value.trim() })
    })
        .then(function(resp) {
            return resp.json().then(function(data) {
                return { ok: resp.ok, data: data };
            });
        })
        .then(function(result) {
            if (result.ok) {
                showToast(result.data.message || "Maintenance window saved", "success");
            } else {
                showToast(result.data.error || "Failed to save maintenance window", "error");
            }
        })
        .catch(function() {
            showToast("Network error -- could not save maintenance window", "error");
        });
}

/* ------------------------------------------------------------
   Backup & Restore
   ------------------------------------------------------------ */

function exportConfig() {
    var includeSecrets = document.getElementById("export-secrets-toggle");
    var qs = (includeSecrets && includeSecrets.checked) ? "?secrets=true" : "";
    fetch("/api/config/export" + qs)
        .then(function(r) {
            if (!r.ok) throw new Error("Export failed");
            return r.blob();
        })
        .then(function(blob) {
            var a = document.createElement("a");
            a.href = URL.createObjectURL(blob);
            a.download = "sentinel-config-" + new Date().toISOString().slice(0, 10) + ".json";
            a.click();
            URL.revokeObjectURL(a.href);
            showToast("Configuration exported", "success");
        })
        .catch(function() {
            showToast("Export failed", "error");
        });
}

function importConfig() {
    var fileInput = document.getElementById("config-import-file");
    if (!fileInput || !fileInput.files.length) {
        showToast("Select a file first", "error");
        return;
    }

    if (!confirm("Import will overwrite matching settings. Continue?")) return;

    var file = fileInput.files[0];
    var reader = new FileReader();
    reader.onload = function(e) {
        fetch("/api/config/import", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: e.target.result
        })
            .then(function(r) { return r.json(); })
            .then(function(data) {
                if (data.error) {
                    showToast(data.error, "error");
                } else {
                    showToast(data.message || "Configuration imported", "success");
                    setTimeout(function() { location.reload(); }, 1000);
                }
            })
            .catch(function() {
                showToast("Import failed", "error");
            });
    };
    reader.readAsText(file);
}

export {
    initSettingsPage,
    onPollIntervalChange,
    onCustomUnitChange,
    applyCustomPollInterval,
    setDefaultPolicy,
    setRollbackPolicy,
    onGracePeriodChange,
    applyCustomGracePeriod,
    setLatestAutoUpdate,
    setPauseState,
    saveFilters,
    setImageCleanup,
    saveCronSchedule,
    setDependencyAware,
    setHooksEnabled,
    setHooksWriteLabels,
    setDryRun,
    setPullOnly,
    setUpdateDelay,
    setComposeSync,
    setImageBackup,
    setShowStopped,
    setRemoveVolumes,
    setScanConcurrency,
    setHADiscovery,
    saveHADiscoveryPrefix,
    updateToggleText,
    toggleCollapsible,
    saveDockerTLS,
    testDockerTLS,
    setWebhookEnabled,
    regenerateWebhookSecret,
    copyWebhookURL,
    copyWebhookSecret,
    saveMaintenanceWindow,
    updateScanPreviews,
    exportConfig,
    importConfig
};
