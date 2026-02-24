/* ============================================================
   Dashboard module — theme, accordions, pause banner, last scan,
   row click, stack toggle, multi-select, filter/sort, tabs,
   manage mode, drag reorder
   ============================================================ */

import { showToast, escapeHTML } from "./utils.js";

/* ------------------------------------------------------------
   1. Theme System
   ------------------------------------------------------------ */

function initTheme() {
    var saved = localStorage.getItem("sentinel-theme") || "auto";
    applyTheme(saved);
}

function applyTheme(theme) {
    var root = document.documentElement;

    if (theme === "dark") {
        root.style.colorScheme = "dark";
    } else if (theme === "light") {
        root.style.colorScheme = "light";
    } else {
        root.style.colorScheme = "dark light";
    }

    localStorage.setItem("sentinel-theme", theme);
}

/* ------------------------------------------------------------
   1b. Accordion Persistence
   ------------------------------------------------------------ */

function getAccordionKey(details) {
    var h2 = details.querySelector("h2");
    if (!h2) return null;
    var page = document.body.dataset.page || window.location.pathname;
    return "sentinel-acc::" + page + "::" + h2.textContent.trim();
}

function saveAccordionState(details) {
    var key = getAccordionKey(details);
    if (!key) return;
    localStorage.setItem(key, details.open ? "1" : "0");
}

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

function initAccordionPersistence() {
    var mode = localStorage.getItem("sentinel-sections") || "remember";
    var accordions = document.querySelectorAll("details.accordion");

    // If there's only one accordion on the page, always expand it.
    var forceOpen = accordions.length === 1;

    for (var i = 0; i < accordions.length; i++) {
        var details = accordions[i];

        if (forceOpen) {
            details.open = true;
        } else if (mode === "remember") {
            // Restore saved state if available
            var key = getAccordionKey(details);
            if (key) {
                var saved = localStorage.getItem(key);
                if (saved === "1") details.open = true;
                else if (saved === "0") details.open = false;
                // If no saved state, keep the HTML default
            }
        } else if (mode === "collapsed") {
            details.open = false;
        } else if (mode === "expanded") {
            details.open = true;
        }

        // Listen for toggle to persist user choices (always, regardless of mode)
        details.addEventListener("toggle", function() {
            if (localStorage.getItem("sentinel-sections") === "remember") {
                saveAccordionState(this);
            }
        });
    }
}

/* ------------------------------------------------------------
   2c. Dashboard Pause Banner
   ------------------------------------------------------------ */

function initPauseBanner() {
    var banner = document.getElementById("pause-banner");
    if (!banner) return;

    fetch("/api/settings")
        .then(function(r) { return r.json(); })
        .then(function(settings) {
            if (settings["paused"] === "true") {
                banner.style.display = "";
            }
        })
        .catch(function() { /* ignore — falls back to defaults */ });
}

function resumeScanning() {
    var banner = document.getElementById("pause-banner");
    fetch("/api/settings/pause", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ paused: false })
    })
        .then(function(resp) {
            return resp.json().then(function(data) {
                return { ok: resp.ok, data: data };
            });
        })
        .then(function(result) {
            if (result.ok) {
                if (banner) banner.style.display = "none";
                showToast("Scanning resumed", "success");
            } else {
                showToast(result.data.error || "Failed to resume scanning", "error");
            }
        })
        .catch(function() {
            showToast("Network error — could not resume scanning", "error");
        });
}

function checkPauseState() {
    var banner = document.getElementById("pause-banner");
    if (!banner) return;

    fetch("/api/settings")
        .then(function(r) { return r.json(); })
        .then(function(settings) {
            banner.style.display = settings["paused"] === "true" ? "" : "none";
        })
        .catch(function() { /* ignore — falls back to defaults */ });
}

/* ------------------------------------------------------------
   2c. Last Scan Timestamp
   ------------------------------------------------------------ */

var lastScanTimestamp = null;
var lastScanTimer = null;

function refreshLastScan() {
    var el = document.getElementById("last-scan");
    if (!el) return;

    fetch("/api/last-scan")
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (!data.last_scan) {
                el.textContent = "Last scan: never";
                lastScanTimestamp = null;
                return;
            }
            lastScanTimestamp = new Date(data.last_scan);
            el.title = lastScanTimestamp.toLocaleString();
            renderLastScanTicker();
            if (!lastScanTimer) {
                lastScanTimer = setInterval(renderLastScanTicker, 1000);
            }
        })
        .catch(function() { /* ignore — falls back to defaults */ });
}

function renderLastScanTicker() {
    var el = document.getElementById("last-scan");
    if (!el || !lastScanTimestamp) return;

    var diff = Math.floor((Date.now() - lastScanTimestamp.getTime()) / 1000);
    if (diff < 0) diff = 0;

    var parts = [];
    if (diff >= 86400) {
        parts.push(Math.floor(diff / 86400) + "d");
        diff %= 86400;
    }
    if (diff >= 3600) {
        parts.push(Math.floor(diff / 3600) + "h");
        diff %= 3600;
    }
    if (diff >= 60) {
        parts.push(Math.floor(diff / 60) + "m");
        diff %= 60;
    }
    parts.push(diff + "s");

    el.textContent = "Last scan: " + parts.join(" ") + " ago";
}

/* ------------------------------------------------------------
   6. Row Click Delegation
   ------------------------------------------------------------ */

function onRowClick(e, name) {
    var tag = e.target.tagName;
    if (tag === "A" || tag === "BUTTON" || tag === "SELECT" || tag === "INPUT" || tag === "OPTION") {
        return;
    }
    if (e.target.closest(".status-badge-wrap")) {
        return;
    }
    var row = e.target.closest("tr.container-row");
    var href = row ? row.getAttribute("data-href") : "";
    if (href) { window.location.href = href; return; }
    var host = row ? row.getAttribute("data-host") : "";
    var url = "/container/" + encodeURIComponent(name);
    if (host) url += "?host=" + encodeURIComponent(host);
    window.location.href = url;
}

/* ------------------------------------------------------------
   7. Stack Toggle
   ------------------------------------------------------------ */

function toggleStack(headerRow) {
    // Non-cluster path: stack is a <tbody class="stack-group">.
    var group = headerRow.closest(".stack-group");
    if (group) {
        group.classList.toggle("stack-collapsed");
        var expanded = !group.classList.contains("stack-collapsed");
        headerRow.setAttribute("aria-expanded", expanded ? "true" : "false");
        return;
    }

    // Cluster path: stack header is a <tr> inside a host-group <tbody>.
    // Toggle visibility of following sibling rows until the next stack-header
    // or host-header.
    var collapsed = headerRow.classList.toggle("stack-section-collapsed");
    var chevron = headerRow.querySelector(".stack-chevron");
    if (chevron) {
        chevron.style.transform = collapsed ? "rotate(0deg)" : "rotate(90deg)";
    }
    var sibling = headerRow.nextElementSibling;
    while (sibling && !sibling.classList.contains("stack-header") && !sibling.classList.contains("host-header")) {
        sibling.style.display = collapsed ? "none" : "";
        sibling = sibling.nextElementSibling;
    }
}

function toggleSwarmSection(row) {
    var section = row.closest(".swarm-section");
    if (!section) return;
    var isCollapsed = section.classList.toggle("swarm-collapsed");
    var icon = section.querySelector(".expand-icon");
    if (icon) icon.textContent = isCollapsed ? "\u25B8" : "\u25BE";
    // Hide/show all following svc-group tbody elements until the next non-svc-group.
    var sibling = section.nextElementSibling;
    while (sibling && sibling.classList.contains("svc-group")) {
        sibling.style.display = isCollapsed ? "none" : "";
        sibling = sibling.nextElementSibling;
    }
}

function toggleHostGroup(header) {
    var hostGroup = header.closest(".host-group");
    if (!hostGroup) return;
    var isCollapsed = hostGroup.classList.toggle("host-collapsed");
    var icon = header.querySelector(".expand-icon");
    if (icon) {
        icon.textContent = isCollapsed ? "\u25B8" : "\u25BE"; // right-pointing or down-pointing triangle
    }
}

function expandAllStacks() {
    var groups = document.querySelectorAll(".stack-group");
    for (var i = 0; i < groups.length; i++) {
        groups[i].classList.remove("stack-collapsed");
        var header = groups[i].querySelector(".stack-header");
        if (header) header.setAttribute("aria-expanded", "true");
    }
    // Expand stacks inside host groups (cluster path).
    var hostStackHeaders = document.querySelectorAll(".host-group .stack-header");
    for (var i = 0; i < hostStackHeaders.length; i++) {
        hostStackHeaders[i].classList.remove("stack-section-collapsed");
        var chevron = hostStackHeaders[i].querySelector(".stack-chevron");
        if (chevron) chevron.style.transform = "rotate(90deg)";
        var sibling = hostStackHeaders[i].nextElementSibling;
        while (sibling && !sibling.classList.contains("stack-header") && !sibling.classList.contains("host-header")) {
            sibling.style.display = "";
            sibling = sibling.nextElementSibling;
        }
    }
    // Expand host groups themselves.
    var hostGroups = document.querySelectorAll(".host-group");
    for (var i = 0; i < hostGroups.length; i++) {
        hostGroups[i].classList.remove("host-collapsed");
        var icon = hostGroups[i].querySelector(".expand-icon");
        if (icon) icon.textContent = "\u25BE";
    }
    // Also expand Swarm service groups.
    var svcGroups = document.querySelectorAll(".svc-group");
    for (var i = 0; i < svcGroups.length; i++) {
        svcGroups[i].classList.remove("svc-collapsed");
    }
}

function collapseAllStacks() {
    var groups = document.querySelectorAll(".stack-group");
    for (var i = 0; i < groups.length; i++) {
        groups[i].classList.add("stack-collapsed");
        var header = groups[i].querySelector(".stack-header");
        if (header) header.setAttribute("aria-expanded", "false");
    }
    // Collapse stacks inside host groups (cluster path).
    var hostStackHeaders = document.querySelectorAll(".host-group .stack-header");
    for (var i = 0; i < hostStackHeaders.length; i++) {
        hostStackHeaders[i].classList.add("stack-section-collapsed");
        var chevron = hostStackHeaders[i].querySelector(".stack-chevron");
        if (chevron) chevron.style.transform = "rotate(0deg)";
        var sibling = hostStackHeaders[i].nextElementSibling;
        while (sibling && !sibling.classList.contains("stack-header") && !sibling.classList.contains("host-header")) {
            sibling.style.display = "none";
            sibling = sibling.nextElementSibling;
        }
    }
    // Collapse host groups themselves.
    var hostGroups = document.querySelectorAll(".host-group");
    for (var i = 0; i < hostGroups.length; i++) {
        hostGroups[i].classList.add("host-collapsed");
        var icon = hostGroups[i].querySelector(".expand-icon");
        if (icon) icon.textContent = "\u25B8";
    }
    // Also collapse Swarm service groups.
    var svcGroups = document.querySelectorAll(".svc-group");
    for (var i = 0; i < svcGroups.length; i++) {
        svcGroups[i].classList.add("svc-collapsed");
    }
}

/* ------------------------------------------------------------
   9. Multi-select
   ------------------------------------------------------------ */

var selectedContainers = {};
var manageMode = false;

function updateSelectionUI() {
    var names = Object.keys(selectedContainers);
    var count = 0;
    for (var i = 0; i < names.length; i++) {
        if (selectedContainers[names[i]]) count++;
    }

    var bar = document.getElementById("bulk-bar");
    var countEl = document.getElementById("bulk-count");
    if (count > 0) {
        if (bar) bar.style.display = "";
        if (countEl) countEl.textContent = count + " selected";
        document.body.style.paddingBottom = "70px";
    } else {
        if (bar) bar.style.display = "none";
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
    var stackCbs = document.querySelectorAll(".stack-select");
    for (var s = 0; s < stackCbs.length; s++) {
        stackCbs[s].checked = false;
        stackCbs[s].indeterminate = false;
    }
    updateSelectionUI();
}

function recomputeSelectionState() {
    // Per-stack: set stack checkbox to checked/unchecked/indeterminate.
    var stacks = document.querySelectorAll("tbody.stack-group");
    for (var s = 0; s < stacks.length; s++) {
        var stackCb = stacks[s].querySelector(".stack-select");
        if (!stackCb) continue;
        var rows = stacks[s].querySelectorAll(".row-select");
        var total = rows.length;
        var checked = 0;
        for (var r = 0; r < rows.length; r++) {
            if (rows[r].checked) checked++;
        }
        stackCb.checked = total > 0 && checked === total;
        stackCb.indeterminate = checked > 0 && checked < total;
    }

    // Global select-all.
    var allRows = document.querySelectorAll(".row-select");
    var allTotal = allRows.length;
    var allChecked = 0;
    for (var a = 0; a < allRows.length; a++) {
        if (allRows[a].checked) allChecked++;
    }
    var selectAll = document.getElementById("select-all");
    if (selectAll) {
        selectAll.checked = allTotal > 0 && allChecked === allTotal;
        selectAll.indeterminate = allChecked > 0 && allChecked < allTotal;
    }

    updateSelectionUI();
}

/* ------------------------------------------------------------
   9a. Filter & Sort System
   ------------------------------------------------------------ */

var filterState = { status: "all", updates: "all", sort: "default" };
var activeDashboardTab = null;

function initFilters() {
    var saved = localStorage.getItem("sentinel-filters");
    if (saved) { try { filterState = JSON.parse(saved); } catch(e) {} }

    // Restore active states from saved filter state.
    var pills = document.querySelectorAll(".filter-pill");
    for (var i = 0; i < pills.length; i++) {
        var p = pills[i];
        if (filterState[p.getAttribute("data-filter")] === p.getAttribute("data-value")) {
            p.classList.add("active");
        }
    }

    var bar = document.getElementById("filter-bar");
    if (bar) bar.addEventListener("click", function(e) {
        var pill = e.target.closest(".filter-pill");
        if (!pill) return;

        var key = pill.getAttribute("data-filter");
        var value = pill.getAttribute("data-value");
        var exclusive = pill.getAttribute("data-exclusive");
        var wasActive = pill.classList.contains("active");

        // Deactivate exclusive siblings and reset their filter state.
        if (exclusive) {
            var siblings = bar.querySelectorAll('.filter-pill[data-exclusive="' + exclusive + '"]');
            for (var s = 0; s < siblings.length; s++) {
                siblings[s].classList.remove("active");
                var sibKey = siblings[s].getAttribute("data-filter");
                if (sibKey !== key) {
                    filterState[sibKey] = sibKey === "sort" ? "default" : "all";
                }
            }
        }

        // Toggle: if was active, deactivate (reset to default); if not, activate.
        if (wasActive) {
            pill.classList.remove("active");
            filterState[key] = key === "sort" ? "default" : "all";
        } else {
            pill.classList.add("active");
            filterState[key] = value;
        }

        localStorage.setItem("sentinel-filters", JSON.stringify(filterState));
        applyFiltersAndSort();
    });

    applyFiltersAndSort();
}

// activateFilter is called from stat card clicks to set a filter + expand all stacks.
function activateFilter(key, value) {
    // Reset all filters first.
    filterState = { status: "all", updates: "all", sort: "default" };

    // Set the requested filter.
    filterState[key] = value;
    localStorage.setItem("sentinel-filters", JSON.stringify(filterState));

    // Update pill UI to match.
    var pills = document.querySelectorAll(".filter-pill");
    for (var i = 0; i < pills.length; i++) {
        var p = pills[i];
        if (p.getAttribute("data-filter") === key && p.getAttribute("data-value") === value) {
            p.classList.add("active");
        } else {
            p.classList.remove("active");
        }
    }

    expandAllStacks();
    applyFiltersAndSort();
}

function applyFiltersAndSort() {
    var table = document.getElementById("container-table");
    if (!table) return;

    // Scope filtering to the active tab; null means no tabs (process everything).
    var activeTab = activeDashboardTab;

    var stacks = table.querySelectorAll("tbody.stack-group");
    for (var s = 0; s < stacks.length; s++) {
        var stack = stacks[s];

        // Skip tbodies belonging to a different tab.
        var stackTab = stack.getAttribute("data-tab");
        if (activeTab !== null && activeTab !== "all" && stackTab !== null && stackTab !== activeTab) continue;

        // Skip tbodies hidden by tab switching.
        if (stack.style.display === "none") continue;

        var rows = stack.querySelectorAll(".container-row");
        var visibleCount = 0;
        for (var r = 0; r < rows.length; r++) {
            var row = rows[r];
            var show = true;
            if (filterState.status === "running") show = row.classList.contains("state-running");
            else if (filterState.status === "stopped") show = !row.classList.contains("state-running");
            if (show && filterState.updates === "pending") show = row.classList.contains("has-update");
            row.style.display = show ? "" : "none";
            if (show) visibleCount++;
        }
        stack.style.display = visibleCount === 0 ? "none" : "";
    }

    // Filter Swarm service groups too.
    var svcGroups = table.querySelectorAll("tbody.svc-group");
    for (var g = 0; g < svcGroups.length; g++) {
        var svcGroup = svcGroups[g];

        // Skip tbodies belonging to a different tab.
        var svcTab = svcGroup.getAttribute("data-tab");
        if (activeTab !== null && activeTab !== "all" && svcTab !== null && svcTab !== activeTab) continue;

        // Skip tbodies hidden by tab switching.
        if (svcGroup.style.display === "none") continue;

        var svcHeader = svcGroup.querySelector(".svc-header");
        if (!svcHeader) continue;
        var showSvc = true;
        if (filterState.status === "running") {
            var rb = svcHeader.querySelector(".svc-replicas");
            showSvc = rb && !rb.classList.contains("svc-replicas-down");
        } else if (filterState.status === "stopped") {
            var rb2 = svcHeader.querySelector(".svc-replicas");
            showSvc = rb2 && rb2.classList.contains("svc-replicas-down");
        }
        if (showSvc && filterState.updates === "pending") {
            showSvc = svcHeader.classList.contains("has-update");
        }
        svcGroup.style.display = showSvc ? "" : "none";
    }

    // Filter host-group tbodies (cluster mode).
    var hostGroups = table.querySelectorAll("tbody.host-group");
    for (var h = 0; h < hostGroups.length; h++) {
        var hg = hostGroups[h];

        var hgTab = hg.getAttribute("data-tab");
        if (activeTab !== null && activeTab !== "all" && hgTab !== null && hgTab !== activeTab) continue;
        if (hg.style.display === "none") continue;

        var hgRows = hg.querySelectorAll(".container-row");
        var hgVisible = 0;
        for (var hr = 0; hr < hgRows.length; hr++) {
            var hRow = hgRows[hr];
            var hShow = true;
            if (filterState.status === "running") hShow = hRow.classList.contains("state-running");
            else if (filterState.status === "stopped") hShow = !hRow.classList.contains("state-running");
            if (hShow && filterState.updates === "pending") hShow = hRow.classList.contains("has-update");
            hRow.style.display = hShow ? "" : "none";
            if (hShow) hgVisible++;
        }
    }

    if (filterState.sort !== "default") {
        sortRows(stacks);
        sortSwarmServices(table);
    }
}

function sortRows(stacks) {
    for (var s = 0; s < stacks.length; s++) {
        var tbody = stacks[s];
        var rows = [];
        var rowEls = tbody.querySelectorAll(".container-row");
        for (var r = 0; r < rowEls.length; r++) {
            if (rowEls[r].style.display === "none") continue;
            rows.push(rowEls[r]);
        }

        if (filterState.sort === "alpha") {
            rows.sort(function(a, b) {
                return a.getAttribute("data-name").localeCompare(b.getAttribute("data-name"));
            });
        } else if (filterState.sort === "status") {
            rows.sort(function(a, b) {
                var sa = statusScore(a), sb = statusScore(b);
                return sa !== sb ? sb - sa : a.getAttribute("data-name").localeCompare(b.getAttribute("data-name"));
            });
        }

        for (var i = 0; i < rows.length; i++) {
            tbody.appendChild(rows[i]);
        }
    }
}

function sortSwarmServices(table) {
    var groups = table.querySelectorAll("tbody.svc-group");
    if (groups.length < 2) return;
    var arr = [];
    for (var i = 0; i < groups.length; i++) {
        if (groups[i].style.display === "none") continue;
        arr.push(groups[i]);
    }
    if (filterState.sort === "alpha") {
        arr.sort(function(a, b) {
            return (a.getAttribute("data-service") || "").localeCompare(b.getAttribute("data-service") || "");
        });
    } else if (filterState.sort === "status") {
        arr.sort(function(a, b) {
            var sa = svcStatusScore(a), sb = svcStatusScore(b);
            return sa !== sb ? sb - sa : (a.getAttribute("data-service") || "").localeCompare(b.getAttribute("data-service") || "");
        });
    }
    for (var j = 0; j < arr.length; j++) {
        arr[j].parentNode.appendChild(arr[j]);
    }
}

function svcStatusScore(svcGroup) {
    var header = svcGroup.querySelector(".svc-header");
    if (!header) return 0;
    var rep = header.querySelector(".svc-replicas");
    if (rep && rep.classList.contains("svc-replicas-down")) return 3;
    if (header.classList.contains("has-update")) return 2;
    return 1;
}

function statusScore(row) {
    if (!row.classList.contains("state-running")) return 3;
    if (row.classList.contains("has-update")) return 2;
    return 1;
}

/* ------------------------------------------------------------
   9b. Dashboard Tabs
   ------------------------------------------------------------ */

function initDashboardTabs() {
    var tabsEl = document.getElementById("dashboard-tabs");
    if (!tabsEl) return; // Standalone mode — no tabs

    var saved = localStorage.getItem("sentinel-dashboard-tab") || "all";

    // Validate saved tab exists; fall back to first tab button if not found.
    var buttons = tabsEl.querySelectorAll(".tab-btn");
    var found = false;
    for (var i = 0; i < buttons.length; i++) {
        if (buttons[i].getAttribute("data-tab") === saved) {
            found = true;
            break;
        }
    }
    if (!found && buttons.length > 0) {
        saved = buttons[0].getAttribute("data-tab");
    }

    switchDashboardTab(saved);

    for (var j = 0; j < buttons.length; j++) {
        buttons[j].addEventListener("click", function() {
            switchDashboardTab(this.getAttribute("data-tab"));
        });
    }
}

function switchDashboardTab(tabId) {
    var tabsEl = document.getElementById("dashboard-tabs");
    if (!tabsEl) return;

    activeDashboardTab = tabId;

    // Update tab button active states and aria-selected attributes.
    var buttons = tabsEl.querySelectorAll(".tab-btn");
    var activeBtn = null;
    for (var i = 0; i < buttons.length; i++) {
        var btn = buttons[i];
        if (btn.getAttribute("data-tab") === tabId) {
            btn.classList.add("active");
            btn.setAttribute("aria-selected", "true");
            activeBtn = btn;
        } else {
            btn.classList.remove("active");
            btn.setAttribute("aria-selected", "false");
        }
    }

    // Update stat cards from the active button's data attributes.
    if (activeBtn) {
        var total   = activeBtn.getAttribute("data-stats-total")   || "0";
        var running = activeBtn.getAttribute("data-stats-running") || "0";
        var pending = activeBtn.getAttribute("data-stats-pending") || "0";
        updateStats(parseInt(total, 10), parseInt(running, 10), parseInt(pending, 10));
    }

    // Show/hide tbody elements based on their data-tab attribute.
    var table = document.getElementById("container-table");
    if (table) {
        var tbodies = table.querySelectorAll("tbody");
        for (var t = 0; t < tbodies.length; t++) {
            var tb = tbodies[t];
            var tbTab = tb.getAttribute("data-tab");
            if (tbTab === null) {
                // No data-tab (e.g. thead-equivalent tbodies) — always visible.
                continue;
            }
            tb.style.display = (tabId === "all" || tbTab === tabId) ? "" : "none";
        }
    }

    localStorage.setItem("sentinel-dashboard-tab", tabId);

    applyFiltersAndSort();
}

function recalcTabStats() {
    var tabsEl = document.getElementById("dashboard-tabs");
    if (!tabsEl) return;

    var table = document.getElementById("container-table");
    if (!table) return;

    var buttons = tabsEl.querySelectorAll(".tab-btn");
    for (var i = 0; i < buttons.length; i++) {
        var btn = buttons[i];
        var tabId = btn.getAttribute("data-tab");

        var total = 0, running = 0, pending = 0;

        // Count stack-group container rows for this tab.
        var stacks = table.querySelectorAll('tbody.stack-group[data-tab="' + tabId + '"]');
        for (var s = 0; s < stacks.length; s++) {
            if (stacks[s].style.display === "none") continue;
            var rows = stacks[s].querySelectorAll(".container-row");
            for (var r = 0; r < rows.length; r++) {
                if (rows[r].style.display === "none") continue;
                total++;
                if (rows[r].classList.contains("state-running")) running++;
                if (rows[r].classList.contains("has-update")) pending++;
            }
        }

        // Count svc-group Swarm services for this tab.
        var svcGroups = table.querySelectorAll('tbody.svc-group[data-tab="' + tabId + '"]');
        for (var g = 0; g < svcGroups.length; g++) {
            if (svcGroups[g].style.display === "none") continue;
            total++;
            var svcHeader = svcGroups[g].querySelector(".svc-header");
            if (svcHeader) {
                var replicas = svcHeader.querySelector(".svc-replicas");
                if (replicas && replicas.classList.contains("svc-replicas-healthy")) running++;
                if (svcHeader.classList.contains("has-update")) pending++;
            }
        }

        // Count host-group container rows (cluster hosts) for this tab.
        var hostGroups = table.querySelectorAll('tbody.host-group[data-tab="' + tabId + '"]');
        for (var hg = 0; hg < hostGroups.length; hg++) {
            if (hostGroups[hg].style.display === "none") continue;
            var hgRows = hostGroups[hg].querySelectorAll(".container-row");
            for (var hr = 0; hr < hgRows.length; hr++) {
                if (hgRows[hr].style.display === "none") continue;
                total++;
                if (hgRows[hr].classList.contains("state-running")) running++;
                if (hgRows[hr].classList.contains("has-update")) pending++;
            }
        }

        btn.setAttribute("data-stats-total",   String(total));
        btn.setAttribute("data-stats-running",  String(running));
        btn.setAttribute("data-stats-pending",  String(pending));

        // Refresh displayed stat cards if this is the active tab.
        if (tabId === activeDashboardTab) {
            updateStats(total, running, pending);
        }

        // Update the badge text for non-"all" tabs immediately.
        if (tabId !== "all") {
            var badge = btn.querySelector(".tab-badge");
            if (badge) badge.textContent = String(total);
        }
    }

    // Update the "all" tab by summing all other tabs.
    var allBtn = tabsEl.querySelector('.tab-btn[data-tab="all"]');
    if (allBtn) {
        var allTotal = 0, allRunning = 0, allPending = 0;
        var nonAllBtns = tabsEl.querySelectorAll(".tab-btn:not([data-tab='all'])");
        for (var k = 0; k < nonAllBtns.length; k++) {
            allTotal += parseInt(nonAllBtns[k].getAttribute("data-stats-total") || "0", 10);
            allRunning += parseInt(nonAllBtns[k].getAttribute("data-stats-running") || "0", 10);
            allPending += parseInt(nonAllBtns[k].getAttribute("data-stats-pending") || "0", 10);
        }
        allBtn.setAttribute("data-stats-total", String(allTotal));
        allBtn.setAttribute("data-stats-running", String(allRunning));
        allBtn.setAttribute("data-stats-pending", String(allPending));
        var allBadge = allBtn.querySelector(".tab-badge");
        if (allBadge) allBadge.textContent = String(allTotal);
        if (activeDashboardTab === "all") {
            updateStats(allTotal, allRunning, allPending);
        }
    }
}

// Forward-declared — will be set by sse.js via setUpdateStatsFn.
// updateStats is defined in sse.js but used by dashboard tabs;
// we import it at usage time to avoid circular deps.
var updateStats = function() {};

function setUpdateStatsFn(fn) {
    updateStats = fn;
}

/* ------------------------------------------------------------
   10. Manage Mode
   ------------------------------------------------------------ */

function toggleManageMode() {
    manageMode = !manageMode;
    var table = document.getElementById("container-table");
    var btn = document.getElementById("manage-btn");
    if (!table || !btn) return;

    if (manageMode) {
        table.classList.add("managing");
        btn.textContent = "Done";
        btn.classList.add("active");
    } else {
        table.classList.remove("managing");
        btn.textContent = "Manage";
        btn.classList.remove("active");
        clearSelection();
    }

    // Enable/disable drag on stack groups
    var groups = table.querySelectorAll(".stack-group");
    for (var i = 0; i < groups.length; i++) {
        if (manageMode) {
            groups[i].setAttribute("draggable", "true");
        } else {
            groups[i].removeAttribute("draggable");
        }
    }
}

/* ------------------------------------------------------------
   Stack Drag Reordering
   ------------------------------------------------------------ */

(function () {
    var dragSrc = null;

    document.addEventListener("dragstart", function (e) {
        var group = e.target.closest(".stack-group");
        if (!group || !manageMode) return;
        // Only allow drag from the handle element.
        if (!e.target.closest(".stack-drag-handle")) {
            e.preventDefault();
            return;
        }
        dragSrc = group;
        group.classList.add("dragging");
        e.dataTransfer.effectAllowed = "move";
        e.dataTransfer.setData("text/plain", group.getAttribute("data-stack"));
    });

    document.addEventListener("dragover", function (e) {
        var group = e.target.closest(".stack-group");
        if (!group || !dragSrc || group === dragSrc) return;
        e.preventDefault();
        e.dataTransfer.dropEffect = "move";

        // Determine above/below by mouse Y relative to element midpoint
        var rect = group.getBoundingClientRect();
        var mid = rect.top + rect.height / 2;
        clearDragClasses(group);
        if (e.clientY < mid) {
            group.classList.add("drag-over-above");
        } else {
            group.classList.add("drag-over-below");
        }
    });

    document.addEventListener("dragleave", function (e) {
        var group = e.target.closest(".stack-group");
        if (group) clearDragClasses(group);
    });

    document.addEventListener("drop", function (e) {
        var group = e.target.closest(".stack-group");
        if (!group || !dragSrc || group === dragSrc) return;
        e.preventDefault();

        var rect = group.getBoundingClientRect();
        var mid = rect.top + rect.height / 2;
        var table = group.closest("table");

        if (e.clientY < mid) {
            table.insertBefore(dragSrc, group);
        } else {
            // Insert after: use nextElementSibling (next tbody)
            var next = group.nextElementSibling;
            if (next) {
                table.insertBefore(dragSrc, next);
            } else {
                table.appendChild(dragSrc);
            }
        }

        cleanupDrag();
        saveStackOrder();
    });

    document.addEventListener("dragend", function () {
        cleanupDrag();
    });

    function clearDragClasses(el) {
        el.classList.remove("drag-over-above", "drag-over-below");
    }

    function cleanupDrag() {
        if (dragSrc) {
            dragSrc.classList.remove("dragging");
            dragSrc = null;
        }
        var all = document.querySelectorAll(".drag-over-above, .drag-over-below");
        for (var i = 0; i < all.length; i++) {
            clearDragClasses(all[i]);
        }
    }

    function saveStackOrder() {
        var groups = document.querySelectorAll(".stack-group");
        var order = [];
        for (var i = 0; i < groups.length; i++) {
            var name = groups[i].getAttribute("data-stack");
            if (name) order.push(name);
        }
        fetch("/api/settings/stack-order", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ order: order })
        })
        .then(function (res) {
            if (!res.ok) throw new Error("save failed");
            showToast("Stack order saved", "success");
        })
        .catch(function () {
            showToast("Failed to save stack order", "error");
        });
    }
})();

export {
    initTheme,
    applyTheme,
    initAccordionPersistence,
    initPauseBanner,
    resumeScanning,
    checkPauseState,
    refreshLastScan,
    onRowClick,
    toggleStack,
    toggleSwarmSection,
    toggleHostGroup,
    expandAllStacks,
    collapseAllStacks,
    selectedContainers,
    manageMode,
    updateSelectionUI,
    clearSelection,
    recomputeSelectionState,
    initFilters,
    activateFilter,
    applyFiltersAndSort,
    initDashboardTabs,
    recalcTabStats,
    setUpdateStatsFn,
    toggleManageMode
};
