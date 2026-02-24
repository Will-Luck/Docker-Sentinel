/* ============================================================
   11. SSE Real-time Updates
   11a. Live Row Updates, GHCR Alternatives
   11b. Digest Banner
   ============================================================ */

import { showToast, queueBatchToast } from "./utils.js";

// Module-level state shared with other modules via window in main.js.
var ghcrAlternatives = {};

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

// Debounced reload for the queue page. Longer delay (2s) so rapid SSE events
// from batch approvals don't cause repeated reloads.
var _queueReloadTimer = null;
function scheduleQueueReload() {
    if (_queueReloadTimer) clearTimeout(_queueReloadTimer);
    _queueReloadTimer = setTimeout(function () {
        _queueReloadTimer = null;
        window.location.reload();
    }, 2000);
}

/* ------------------------------------------------------------
   11a. Live Row Updates — targeted DOM patching via partial endpoint
   ------------------------------------------------------------ */

function updateContainerRow(name, hostId) {
    var enc = encodeURIComponent(name);
    var url = "/api/containers/" + enc + "/row";
    if (hostId) url += "?host=" + encodeURIComponent(hostId);
    fetch(url)
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (!data.html) return;

            // Update stats counters.
            updateStats(data.total, data.running, data.pending);

            // Parse the server-rendered HTML (from Go templates, not user input)
            // into DOM nodes using a temporary tbody element.
            var temp = document.createElement("tbody");
            temp.innerHTML = data.html; // Safe: server-rendered Go template HTML, no user content

            var selector = 'tr.container-row[data-name="' + name + '"]';
            if (hostId) {
                selector += '[data-host="' + hostId + '"]';
            } else {
                selector += '[data-host=""]';
            }
            var oldRow = document.querySelector(selector);

            if (oldRow) {
                var newRow = temp.querySelector(".container-row");

                if (newRow) {
                    oldRow.replaceWith(newRow);
                    newRow.classList.add("row-updated");
                }

                // Reapply checkbox state from selectedContainers after DOM patch.
                var newCb = newRow ? newRow.querySelector(".row-select") : null;
                var _selCont = window._dashboardSelectedContainers || {};
                if (newCb && _selCont[newCb.value]) {
                    newCb.checked = true;
                }
                if (window.recomputeSelectionState) window.recomputeSelectionState();
            }

            // Reapply badges and filters after DOM patch.
            applyRegistryBadges();
            if (window.applyFiltersAndSort) window.applyFiltersAndSort();
            if (window.recalcTabStats) window.recalcTabStats();

            // Clear completed action and re-apply spinners for still-pending ones.
            clearPendingBadge(name, hostId);
            reapplyBadgeSpinners();

            // Preserve update button loading state across row replacement.
            // Only keep spinning if the server confirms update is in-flight
            // (template renders .btn-warning.loading when Maintenance=true).
            if (window._updateLoadingBtns) {
                var updKey = (hostId || "") + "::" + name;
                if (window._updateLoadingBtns[updKey]) {
                    var sel = 'tr.container-row[data-name="' + name + '"]';
                    if (hostId) sel += '[data-host="' + hostId + '"]';
                    var row = document.querySelector(sel);
                    var updBtn = row ? row.querySelector(".btn-warning.loading") : null;
                    if (updBtn) {
                        // Server says update still in-flight — keep tracking.
                        window._updateLoadingBtns[updKey] = updBtn;
                    } else {
                        // Update done (success/failure) or no button — clean up.
                        delete window._updateLoadingBtns[updKey];
                    }
                }
            }
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

var pendingBadgeActions = {};

function showBadgeSpinner(wrap) {
    wrap.setAttribute("data-pending", "");
}

function reapplyBadgeSpinners() {
    for (var key in pendingBadgeActions) {
        var parts = key.split("::");
        var h = parts[0], n = parts[1];
        var selector = '.status-badge-wrap[data-name="' + n + '"]';
        if (h) selector += '[data-host-id="' + h + '"]';
        var wrap = document.querySelector(selector);
        if (wrap) wrap.setAttribute("data-pending", "");
    }
}

function clearPendingBadge(name, hostId) {
    var key = (hostId || "") + "::" + name;
    delete pendingBadgeActions[key];
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
        if (localStorage.getItem("sentinel-self-updating")) {
            localStorage.removeItem("sentinel-self-updating");
            window.location.reload();
            return;
        }
        setConnectionStatus(true);
    });

    es.addEventListener("container_update", function (e) {
        try {
            var data = JSON.parse(e.data);
            // Show toast with appropriate severity.
            var toastType = "info";
            if (data.message) {
                if (data.message.indexOf("failed") !== -1) toastType = "error";
                else if (data.message.indexOf("success") !== -1) toastType = "success";
            }
            queueBatchToast(data.message || ("Update: " + data.container_name), toastType);
            if (data.container_name) {
                updateContainerRow(data.container_name, data.host_id);
                return;
            }
        } catch (_) {}
        scheduleReload();
    });

    es.addEventListener("container_state", function (e) {
        try {
            var data = JSON.parse(e.data);
            if (data.container_name) {
                updateContainerRow(data.container_name, data.host_id);
                return;
            }
        } catch (_) {}
        scheduleReload();
    });

    es.addEventListener("queue_change", function (e) {
        var data;
        try {
            data = JSON.parse(e.data);
            queueBatchToast(data.message || "Queue updated", "info");
        } catch (_) { data = {}; }
        if (document.getElementById("container-table")) {
            // Dashboard: refresh the affected row so UPDATE AVAILABLE appears.
            if (data.container_name) {
                updateContainerRow(data.container_name, data.host_id);
            }
            refreshDashboardStats();
            scheduleDigestBannerRefresh();
            updateQueueBadge();
        } else if (document.querySelector(".queue-table") && !(window._queueBulkInProgress && window._queueBulkInProgress())) {
            scheduleQueueReload();
        } else {
            updateQueueBadge();
        }
    });

    es.addEventListener("scan_complete", function (e) {
        // Clear scan button spinner.
        var scanBtn = document.getElementById("scan-btn");
        if (scanBtn) { scanBtn.classList.remove("loading"); scanBtn.disabled = false; }
        // Re-check pause state on scan events.
        if (window.checkPauseState) window.checkPauseState();
        if (window.refreshLastScan) window.refreshLastScan();
        // On dashboard, rows are already patched by container_update events.
        // Refresh stats and banner to ensure counts are accurate.
        if (document.getElementById("container-table")) {
            refreshDashboardStats();
            if (window.recalcTabStats) window.recalcTabStats();
            scheduleDigestBannerRefresh();
        } else {
            scheduleReload();
        }
        // Show warning toast if containers were skipped or failed.
        var msg = e.data || "";
        var rl = (msg.match(/rate_limited=(\d+)/) || [])[1] | 0;
        var failed = (msg.match(/failed=(\d+)/) || [])[1] | 0;
        if (rl > 0 || failed > 0) {
            var parts = [];
            if (rl > 0) parts.push(rl + " skipped (rate limit)");
            if (failed > 0) parts.push(failed + " failed");
            showToast("Scan complete \u2014 " + parts.join(", "), "warning");
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
        if (window.checkPauseState) window.checkPauseState();
    });

    es.addEventListener("policy_change", function (e) {
        try {
            var data = JSON.parse(e.data);
            showToast(data.message || ("Policy changed: " + data.container_name), "info");
            if (data.container_name) {
                updateContainerRow(data.container_name, data.host_id);
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
                if (window.refreshServiceRow) window.refreshServiceRow(data.container_name);
                // Swarm updates converge over 10-30s; do a second refresh.
                setTimeout(function() { if (window.refreshServiceRow) window.refreshServiceRow(data.container_name); }, 10000);
            }
        } catch (_) {}
    });

    es.addEventListener("ghcr_check", function() {
        loadGHCRAlternatives();
    });

    es.addEventListener("cluster_host", function(e) {
        try {
            var data = JSON.parse(e.data);
            var hostID = data.host_id || data.host_name || "";
            var msg = data.message || "";
            var offline = msg.indexOf("disconnected") !== -1;
            var group = document.querySelector('tbody.host-group[data-host="' + hostID + '"]');
            if (!group) return;
            var header = group.querySelector(".host-header");
            var dot = group.querySelector(".host-status-dot");
            var inner = group.querySelector(".host-header-inner");
            var existing = group.querySelector(".host-offline-link");
            if (header) {
                if (offline) header.classList.add("host-offline");
                else header.classList.remove("host-offline");
            }
            if (dot) {
                dot.className = "host-status-dot " + (offline ? "disconnected" : "connected");
            }
            if (offline && !existing && inner) {
                var link = document.createElement("a");
                link.href = "/cluster";
                link.className = "host-offline-link";
                link.textContent = "OFFLINE \u2014 TROUBLESHOOT";
                link.onclick = function(ev) { ev.stopPropagation(); };
                var count = inner.querySelector(".host-count");
                inner.insertBefore(link, count);
            } else if (!offline && existing) {
                existing.remove();
            }
        } catch (_) {}
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

// Update the queue badge count in the nav. Uses the lightweight /api/queue/count
// endpoint to avoid blocking on release notes lookups. Debounced so rapid
// calls (e.g. bulk row removals) collapse into a single fetch.
var _queueBadgeTimer = null;
function updateQueueBadge() {
    if (_queueBadgeTimer) clearTimeout(_queueBadgeTimer);
    _queueBadgeTimer = setTimeout(function () {
        _queueBadgeTimer = null;
        fetch("/api/queue/count", {credentials: "same-origin"})
            .then(function (r) { return r.json(); })
            .then(function (data) {
                var badges = document.querySelectorAll(".nav-badge");
                var count = typeof data.count === "number" ? data.count : 0;
                for (var i = 0; i < badges.length; i++) {
                    var link = badges[i].closest("a");
                    if (link && link.href && link.href.indexOf("/queue") !== -1) {
                        badges[i].textContent = count;
                        badges[i].style.display = count > 0 ? "" : "none";
                    }
                }
            })
            .catch(function () {});
    }, 300);
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


export {
    scheduleReload,
    scheduleQueueReload,
    updateContainerRow,
    updateStats,
    refreshDashboardStats,
    updatePendingColor,
    showBadgeSpinner,
    reapplyBadgeSpinners,
    clearPendingBadge,
    setConnectionStatus,
    initSSE,
    ghcrAlternatives,
    loadGHCRAlternatives,
    applyRegistryBadges,
    applyGHCRBadges,
    parseDockerRepo,
    renderGHCRAlternatives,
    loadDigestBanner,
    scheduleDigestBannerRefresh,
    updateQueueBadge,
    dismissDigestBanner,
    pendingBadgeActions
};
