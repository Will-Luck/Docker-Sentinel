/* ============================================================
   5. API Actions — Queue operations, container actions, bulk
   ============================================================ */

import { showToast, escapeHTML, showConfirm, apiPost } from "./utils.js";

// Local escapeHtml (lowercase h) used by loadAllTags.
function escapeHtml(str) {
    if (!str) return "";
    return str
        .replace(/&/g, "&amp;")
        .replace(/</g, "&lt;")
        .replace(/>/g, "&gt;")
        .replace(/"/g, "&quot;")
        .replace(/'/g, "&#039;");
}

// Access via window to avoid circular import with sse.js.
function _updateQueueBadge() {
    if (window.updateQueueBadge) window.updateQueueBadge();
}

// Access dashboard state via window to avoid circular imports.
function _getSelectedContainers() { return window._dashboardSelectedContainers || {}; }
function _getManageMode() { return window._dashboardManageMode || false; }

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

        _updateQueueBadge();

        // Show empty state if queue is now empty.
        if (remaining === 0 && tbody) {
            var emptyRow = document.createElement("tr");
            var td = document.createElement("td");
            td.setAttribute("colspan", "5");
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

function toggleQueueAccordion(index) {
    var panel = document.getElementById("accordion-queue-" + index);
    if (!panel) return;
    var visible = panel.style.display !== "none";
    panel.style.display = visible ? "none" : "";
    var row = panel.previousElementSibling;
    var chevron = row ? row.querySelector(".queue-expand") : null;
    if (chevron) chevron.textContent = visible ? "\u25B8" : "\u25BE";
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

// Bulk queue actions — fire staggered API calls, disable all buttons, show a
// single summary toast, and reload the page when done.
var _bulkInProgress = false;

function bulkQueueAction(apiPath, actionLabel, triggerBtn) {
    var rows = document.querySelectorAll(".table-wrap tbody tr.container-row[data-queue-key]");
    if (!rows.length) return;

    _bulkInProgress = true;

    // Disable every button on the page to prevent double-clicks.
    var allBtns = document.querySelectorAll(".queue-table .btn, .queue-header .btn");
    for (var i = 0; i < allBtns.length; i++) {
        allBtns[i].disabled = true;
    }
    if (triggerBtn) {
        triggerBtn.classList.add("loading");
    }

    var keys = [];
    for (var j = 0; j < rows.length; j++) {
        var key = rows[j].getAttribute("data-queue-key");
        if (key) keys.push(key);
    }
    if (!keys.length) {
        _bulkInProgress = false;
        if (triggerBtn) { triggerBtn.classList.remove("loading"); triggerBtn.disabled = false; }
        return;
    }

    var completed = 0;
    var failed = 0;
    var total = keys.length;

    function onDone() {
        if (completed + failed < total) return;
        _bulkInProgress = false;
        if (failed > 0) {
            showToast(completed + " " + actionLabel + ", " + failed + " failed", "warning");
        } else {
            showToast("All " + completed + " updates " + actionLabel, "success");
        }
        setTimeout(function () { window.location.reload(); }, 400);
    }

    for (var k = 0; k < keys.length; k++) {
        (function (queueKey, delay) {
            setTimeout(function () {
                fetch("/api/" + apiPath + "/" + encodeURIComponent(queueKey), {
                    method: "POST",
                    credentials: "same-origin"
                })
                .then(function (r) { return r.json(); })
                .then(function (data) {
                    if (data.error) { failed++; } else { completed++; }
                })
                .catch(function () { failed++; })
                .then(onDone);
            }, delay);
        })(keys[k], k * 100);
    }
}

function approveAll(event) {
    var btn = event && event.target ? event.target.closest(".btn") : null;
    bulkQueueAction("approve", "approved", btn);
}

function ignoreAll(event) {
    var btn = event && event.target ? event.target.closest(".btn") : null;
    bulkQueueAction("ignore", "ignored", btn);
}

function rejectAll(event) {
    var btn = event && event.target ? event.target.closest(".btn") : null;
    bulkQueueAction("reject", "rejected", btn);
}

function triggerUpdate(name, event, hostId) {
    var btn = event && event.target ? event.target.closest(".btn") : null;
    var url = "/api/update/" + encodeURIComponent(name);
    if (hostId) url += "?host=" + encodeURIComponent(hostId);
    // Same pattern as triggerSvcUpdate: manage loading state ourselves,
    // don't pass btn to apiPost, let SSE handle the row refresh.
    if (btn) {
        btn.classList.add("loading");
        btn.disabled = true;
        window._updateLoadingBtns = window._updateLoadingBtns || {};
        var key = (hostId || "") + "::" + name;
        window._updateLoadingBtns[key] = btn;
        setTimeout(function() {
            if (window._updateLoadingBtns && window._updateLoadingBtns[key]) {
                window._updateLoadingBtns[key].classList.remove("loading");
                window._updateLoadingBtns[key].disabled = false;
                delete window._updateLoadingBtns[key];
            }
        }, 120000);
    }
    apiPost(url, null,
        "Update started for " + name,
        "Failed to trigger update"
    );
}

function triggerCheck(name, event, hostId) {
    var btn = event && event.target ? event.target.closest(".btn") : null;
    var url = "/api/check/" + encodeURIComponent(name);
    if (hostId) url += "?host=" + encodeURIComponent(hostId);
    apiPost(
        url,
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

function changePolicy(name, newPolicy, hostId) {
    var url = "/api/containers/" + encodeURIComponent(name) + "/policy";
    if (hostId) url += "?host=" + encodeURIComponent(hostId);
    apiPost(
        url,
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
        localStorage.setItem("sentinel-self-updating", "1");
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

function loadAllTags(summaryEl) {
    var details = summaryEl.parentElement;
    // Only fetch once — after first load the body is populated.
    if (details.dataset.tagsLoaded) return;
    details.dataset.tagsLoaded = "1";

    var name = window._containerName;
    if (!name) return;

    var body = document.getElementById("all-tags-body");
    var preview = document.getElementById("all-tags-preview");

    fetch("/api/containers/" + encodeURIComponent(name) + "/tags")
        .then(function(r) { return r.json(); })
        .then(function(tags) {
            if (!tags || tags.length === 0) {
                if (body) body.innerHTML = '<div class="accordion-empty">No tags found.</div>';
                if (preview) preview.textContent = "None";
                return;
            }
            if (preview) preview.textContent = tags.length + " tags";
            var html = '<div class="tag-list">';
            for (var i = 0; i < tags.length; i++) {
                html += '<span class="badge badge-muted tag-item">' + escapeHtml(tags[i]) + '</span>';
            }
            html += '</div>';
            if (body) body.innerHTML = html;
        })
        .catch(function() {
            if (body) body.innerHTML = '<div class="accordion-empty">Failed to load tags.</div>';
            if (preview) preview.textContent = "Error";
        });
}

function updateToVersion(name, hostId) {
    var sel = document.getElementById("version-select");
    if (!sel || !sel.value) return;
    var tag = sel.value;
    var url = "/api/containers/" + encodeURIComponent(name) + "/update-to-version";
    if (hostId) url += "?host=" + encodeURIComponent(hostId);
    showConfirm("Update to Version",
        "<p>Update <strong>" + name + "</strong> to <code>" + tag + "</code>?</p>"
    ).then(function(confirmed) {
        if (!confirmed) return;
        fetch(url, {
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
    var selectedContainers = _getSelectedContainers();
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
                            if (window.updateContainerRow) window.updateContainerRow(preview.changes[r].name);
                        }

                        // Clear selection and exit manage mode.
                        if (window.clearSelection) window.clearSelection();
                        if (_getManageMode() && window.toggleManageMode) window.toggleManageMode();
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


function getBulkInProgress() { return _bulkInProgress; }

export {
    removeQueueRow,
    toggleQueueAccordion,
    approveUpdate,
    ignoreUpdate,
    rejectUpdate,
    getBulkInProgress,
    bulkQueueAction,
    approveAll,
    ignoreAll,
    rejectAll,
    triggerUpdate,
    triggerCheck,
    triggerRollback,
    changePolicy,
    triggerScan,
    triggerSelfUpdate,
    switchToGHCR,
    loadAllTags,
    updateToVersion,
    applyBulkPolicy
};
