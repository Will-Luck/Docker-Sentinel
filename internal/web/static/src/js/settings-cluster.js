/* ============================================================
   2b. Cluster Settings Tab
   ============================================================ */

import { showToast } from "./utils.js";

// Import updateToggleText from settings-core via window to avoid bundler issues.
function _updateToggleText(textId, enabled) {
    if (window.updateToggleText) {
        window.updateToggleText(textId, enabled);
    } else {
        var text = document.getElementById(textId);
        if (text) text.textContent = enabled ? "On" : "Off";
    }
}

function loadClusterSettings() {
    // Only run on the settings page (cluster-enabled element exists).
    if (!document.getElementById("cluster-enabled")) return;

    fetch("/api/settings/cluster")
        .then(function(r) { return r.json(); })
        .then(function(s) {
            var enabled = s.enabled === "true";
            document.getElementById("cluster-enabled").checked = enabled;
            _updateToggleText("cluster-enabled-text", enabled);
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
    _updateToggleText("cluster-enabled-text", enabled);
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
            showToast("Network error -- could not save cluster settings", "error");
        });
}

export {
    loadClusterSettings,
    onClusterToggle,
    toggleClusterFields,
    saveClusterSettings
};
