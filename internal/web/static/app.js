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
                refreshContent();
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
                refreshContent();
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
                refreshContent();
            } else {
                showToast(result.data.error || "Failed to trigger update", "error");
            }
        })
        .catch(function () {
            showToast("Network error — could not trigger update", "error");
        });
}

// Refresh the main content area after an action.
function refreshContent() {
    // Trigger htmx to re-fetch tables if present.
    var tables = document.querySelectorAll("[hx-get]");
    tables.forEach(function (el) {
        if (typeof htmx !== "undefined") {
            htmx.trigger(el, "refresh");
        }
    });

    // Fallback: reload after a short delay to let the server process.
    setTimeout(function () {
        window.location.reload();
    }, 1500);
}

// htmx configuration — listen for errors.
document.addEventListener("DOMContentLoaded", function () {
    if (typeof htmx !== "undefined") {
        htmx.config.defaultSwapStyle = "innerHTML";

        document.body.addEventListener("htmx:responseError", function (event) {
            showToast("Failed to load data from server", "error");
        });

        document.body.addEventListener("htmx:sendError", function () {
            showToast("Could not reach server", "error");
        });
    }
});
