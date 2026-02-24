/* ============================================================
   15. Registry Credentials & Rate Limits
   16. Rate Limit Status - Dashboard Polling
   ============================================================ */

import { showToast } from "./utils.js";

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

if (document.getElementById("rate-limit-status")) {
    updateRateLimitStatus();
}


export {
    loadRegistries,
    addRegistryCredential,
    saveRegistryCredentials,
    updateRateLimitStatus
};
