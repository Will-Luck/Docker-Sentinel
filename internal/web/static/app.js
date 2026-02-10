/* Docker-Sentinel Dashboard — Client-side JavaScript */

// Toast notification system.

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
            toast.remove();
        }, 300);
    }, 4000);
}

// HTML escaping helper.

function escapeHTML(str) {
    var div = document.createElement("div");
    div.appendChild(document.createTextNode(str));
    return div.innerHTML;
}

// API action helpers.

function approveUpdate(name) {
    fetch("/api/approve/" + encodeURIComponent(name), { method: "POST" })
        .then(function (resp) {
            return resp.json().then(function (data) {
                return { ok: resp.ok, data: data };
            });
        })
        .then(function (result) {
            if (result.ok) {
                showToast("Approved update for " + name, "success");
            } else {
                showToast(result.data.error || "Failed to approve", "error");
            }
        })
        .catch(function () {
            showToast("Network error — could not approve", "error");
        });
}

function rejectUpdate(name) {
    fetch("/api/reject/" + encodeURIComponent(name), { method: "POST" })
        .then(function (resp) {
            return resp.json().then(function (data) {
                return { ok: resp.ok, data: data };
            });
        })
        .then(function (result) {
            if (result.ok) {
                showToast("Rejected update for " + name, "info");
            } else {
                showToast(result.data.error || "Failed to reject", "error");
            }
        })
        .catch(function () {
            showToast("Network error — could not reject", "error");
        });
}

function triggerUpdate(name) {
    fetch("/api/update/" + encodeURIComponent(name), { method: "POST" })
        .then(function (resp) {
            return resp.json().then(function (data) {
                return { ok: resp.ok, data: data };
            });
        })
        .then(function (result) {
            if (result.ok) {
                showToast("Update started for " + name, "success");
            } else {
                showToast(result.data.error || "Failed to trigger update", "error");
            }
        })
        .catch(function () {
            showToast("Network error — could not trigger update", "error");
        });
}

function triggerRollback(name) {
    fetch("/api/containers/" + encodeURIComponent(name) + "/rollback", { method: "POST" })
        .then(function (resp) {
            return resp.json().then(function (data) {
                return { ok: resp.ok, data: data };
            });
        })
        .then(function (result) {
            if (result.ok) {
                showToast("Rollback started for " + name, "success");
            } else {
                showToast(result.data.error || "Failed to trigger rollback", "error");
            }
        })
        .catch(function () {
            showToast("Network error — could not trigger rollback", "error");
        });
}

function changePolicy(name, newPolicy) {
    fetch("/api/containers/" + encodeURIComponent(name) + "/policy", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ policy: newPolicy })
    })
        .then(function (resp) {
            return resp.json().then(function (data) {
                return { ok: resp.ok, data: data };
            });
        })
        .then(function (result) {
            if (result.ok) {
                showToast("Policy change to " + newPolicy + " started for " + name, "success");
            } else {
                showToast(result.data.error || "Failed to change policy", "error");
            }
        })
        .catch(function () {
            showToast("Network error — could not change policy", "error");
        });
}

function updateToVersion(name) {
    var sel = document.getElementById("version-select");
    if (!sel) return;
    var version = sel.value;
    showToast("Version pinning to " + version + " is not yet implemented", "info");
}

// --- Stack toggle ---

function toggleStack(headerRow) {
    var tbody = headerRow.closest(".stack-group");
    if (!tbody) return;
    tbody.classList.toggle("stack-collapsed");
}

// --- Accordion ---

var accordionCache = {};

function toggleAccordion(name, btn) {
    var panel = document.getElementById("accordion-" + name);
    if (!panel) return;

    var isOpen = panel.style.display !== "none";
    if (isOpen) {
        panel.style.display = "none";
        if (btn) btn.textContent = "+";
        return;
    }

    panel.style.display = "";
    if (btn) btn.textContent = "\u2212"; // minus sign

    // Use cache if available.
    if (accordionCache[name]) {
        renderAccordionContent(name, accordionCache[name]);
        return;
    }

    // Lazy-load from API.
    var contentEl = panel.querySelector(".accordion-content");
    contentEl.textContent = "Loading...";

    var enc = encodeURIComponent(name);
    Promise.all([
        fetch("/api/containers/" + enc).then(function (r) { return r.json(); }),
        fetch("/api/containers/" + enc + "/versions").then(function (r) { return r.json(); })
    ]).then(function (results) {
        var detail = results[0];
        var versions = results[1];
        var data = { detail: detail, versions: versions };
        accordionCache[name] = data;
        renderAccordionContent(name, data);
    }).catch(function () {
        contentEl.textContent = "Failed to load data";
    });
}

function renderAccordionContent(name, data) {
    var panel = document.getElementById("accordion-" + name);
    if (!panel) return;
    var contentEl = panel.querySelector(".accordion-content");
    var d = data.detail;
    var versions = data.versions || [];

    // Build DOM safely without innerHTML.
    while (contentEl.firstChild) contentEl.removeChild(contentEl.firstChild);

    var grid = document.createElement("div");
    grid.className = "accordion-grid";

    // Info section
    var infoSection = document.createElement("div");
    infoSection.className = "accordion-section";

    function addField(parent, label, value, extraClass) {
        var lbl = document.createElement("div");
        lbl.className = "accordion-label";
        lbl.textContent = label;
        parent.appendChild(lbl);
        var val = document.createElement("div");
        val.className = "accordion-value" + (extraClass ? " " + extraClass : "");
        val.textContent = value;
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

    // Versions section
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

    // Actions section
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

// --- Multi-select ---

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
        bar.style.display = "";
        countEl.textContent = count + " selected";
        document.body.style.paddingBottom = "70px";
    } else {
        bar.style.display = "none";
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

// --- SSE real-time updates ---

var sseReloadTimer = null;

function scheduleReload() {
    // Clear accordion cache so stale data isn't shown after reload.
    accordionCache = {};
    // Debounce: batch rapid events into a single reload after 800ms of quiet.
    if (sseReloadTimer) clearTimeout(sseReloadTimer);
    sseReloadTimer = setTimeout(function () {
        window.location.reload();
    }, 800);
}

function setConnectionStatus(connected) {
    var dot = document.getElementById("sse-indicator");
    if (!dot) return;
    if (connected) {
        dot.className = "connection-dot connected";
        dot.title = "Live";
    } else {
        dot.className = "connection-dot disconnected";
        dot.title = "Reconnecting...";
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
        // EventSource will auto-reconnect.
    };
}

// --- Initialisation ---

document.addEventListener("DOMContentLoaded", function () {
    initSSE();

    // Multi-select: checkbox delegation.
    var table = document.getElementById("container-table");
    if (table) {
        table.addEventListener("change", function (e) {
            var target = e.target;

            // Select-all checkbox
            if (target.id === "select-all") {
                var checkboxes = table.querySelectorAll(".row-select");
                for (var i = 0; i < checkboxes.length; i++) {
                    // Only affect visible (non-collapsed) checkboxes.
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
