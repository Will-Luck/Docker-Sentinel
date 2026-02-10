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

// --- SSE real-time updates ---

var sseReloadTimer = null;

function scheduleReload() {
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

    if (typeof htmx !== "undefined") {
        htmx.config.defaultSwapStyle = "innerHTML";

        document.body.addEventListener("htmx:responseError", function () {
            showToast("Failed to load data from server", "error");
        });

        document.body.addEventListener("htmx:sendError", function () {
            showToast("Could not reach server", "error");
        });
    }
});
