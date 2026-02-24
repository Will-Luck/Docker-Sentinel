/* ============================================================
   13. Notification Configuration - Multi-channel System
   14. Digest Settings
   14b. HTML Escape Helper (local version)
   ============================================================ */

import { showToast, apiPost } from "./utils.js";

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
        { key: "priority", label: "Priority", type: "text", placeholder: "3" },
        { key: "token", label: "Token", type: "password", placeholder: "Bearer token (optional)" },
        { key: "username", label: "Username", type: "text", placeholder: "Username (optional)" },
        { key: "password", label: "Password", type: "password", placeholder: "Password (optional)" }
    ],
    telegram: [
        { key: "bot_token", label: "Bot Token", type: "password", placeholder: "123456:ABC-DEF..." },
        { key: "chat_id", label: "Chat ID", type: "text", placeholder: "-1001234567890" }
    ],
    pushover: [
        { key: "app_token", label: "App Token", type: "password", placeholder: "Application token" },
        { key: "user_key", label: "User Key", type: "password", placeholder: "User/group key" }
    ],
    smtp: [
        { key: "host", label: "SMTP Server", type: "text", placeholder: "smtp.example.com" },
        { key: "port", label: "Port", type: "text", placeholder: "587" },
        { key: "from", label: "From", type: "text", placeholder: "sentinel@example.com" },
        { key: "to", label: "To", type: "text", placeholder: "you@example.com" },
        { key: "username", label: "Username", type: "text", placeholder: "Username (optional)" },
        { key: "password", label: "Password", type: "password", placeholder: "Password (optional)" },
        { key: "tls", label: "Use TLS", type: "text", placeholder: "true or false" }
    ],
    apprise: [
        { key: "url", label: "Apprise API URL", type: "text", placeholder: "http://apprise:8000" },
        { key: "tag", label: "Config Tag", type: "text", placeholder: "Tag for persistent config (optional)" },
        { key: "urls", label: "Apprise URLs", type: "text", placeholder: "Apprise URL(s) for stateless mode (optional)" }
    ],
    mqtt: [
        { key: "broker", label: "Broker URL", type: "text", placeholder: "tcp://mqtt:1883" },
        { key: "topic", label: "Topic", type: "text", placeholder: "sentinel/events" },
        { key: "client_id", label: "Client ID", type: "text", placeholder: "docker-sentinel (optional)" },
        { key: "username", label: "Username", type: "text", placeholder: "Username (optional)" },
        { key: "password", label: "Password", type: "password", placeholder: "Password (optional)" },
        { key: "qos", label: "QoS", type: "text", placeholder: "0, 1, or 2 (default: 0)" }
    ],
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


export {
    loadNotificationChannels,
    addChannel,
    saveNotificationChannels,
    testNotification,
    onNotifyModeChange,
    loadDigestSettings,
    saveDigestSettings,
    triggerDigest,
    loadContainerNotifyPrefs,
    setContainerNotifyPref,
    escapeHtml,
    isSafeURL
};
