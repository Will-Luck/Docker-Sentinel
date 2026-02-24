/* ============================================================
   7b. Swarm Service Toggle & Actions
   ============================================================ */

import { showToast, apiPost } from "./utils.js";

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
function isSafeURL(url) {
    return typeof url === "string" && (url.indexOf("https://") === 0 || url.indexOf("http://") === 0);
}

// showBadgeSpinner from sse.js — inlined to avoid circular deps.
function showBadgeSpinner(wrap) {
    wrap.setAttribute("data-pending", "");
}

// applyRegistryBadges from sse.js — access via window.
function _applyRegistryBadges() {
    if (window.applyRegistryBadges) window.applyRegistryBadges();
}

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
                wrap.style.pointerEvents = "";
                wrap.removeAttribute("data-pending");
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
                    _applyRegistryBadges();
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


export {
    toggleSvc,
    triggerSvcUpdate,
    changeSvcPolicy,
    rollbackSvc,
    scaleSvc,
    refreshServiceRow
};
