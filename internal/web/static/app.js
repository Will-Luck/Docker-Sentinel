(() => {
  // internal/web/static/src/js/csrf.js
  var originalFetch = window.fetch;
  function getCSRFToken() {
    var match = document.cookie.match(/(^|;\s*)sentinel_csrf=([^;]+)/);
    return match ? match[2] : "";
  }
  window.fetch = function(url, opts) {
    opts = opts || {};
    var method = (opts.method || "GET").toUpperCase();
    if (method !== "GET" && method !== "HEAD" && method !== "OPTIONS") {
      var token = getCSRFToken();
      if (token) {
        opts.headers = opts.headers || {};
        if (typeof opts.headers.set === "function") {
          opts.headers.set("X-CSRF-Token", token);
        } else {
          opts.headers["X-CSRF-Token"] = token;
        }
      }
    }
    return originalFetch.call(window, url, opts).then(function(resp) {
      if (resp.status === 401 && url.indexOf("/api/auth/me") === -1) {
        window.location.href = "/login";
      }
      return resp;
    });
  };

  // internal/web/static/src/js/utils.js
  var _toastBatch = [];
  var _toastBatchTimer = null;
  var _toastBatchWindow = 1500;
  function showToast(message, type) {
    _showToastImmediate(message, type);
  }
  function _showToastImmediate(message, type) {
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
    setTimeout(function() {
      toast.style.animation = "fadeOut 0.3s ease-out forwards";
      setTimeout(function() {
        if (toast.parentNode) toast.parentNode.removeChild(toast);
      }, 300);
    }, 4e3);
  }
  function queueBatchToast(message, type) {
    _toastBatch.push({ message, type });
    if (_toastBatchTimer) clearTimeout(_toastBatchTimer);
    _toastBatchTimer = setTimeout(function() {
      _flushBatchToasts();
    }, _toastBatchWindow);
  }
  function _flushBatchToasts() {
    var batch = _toastBatch;
    _toastBatch = [];
    _toastBatchTimer = null;
    if (batch.length === 0) return;
    if (batch.length === 1) {
      _showToastImmediate(batch[0].message, batch[0].type);
      return;
    }
    var updates = 0;
    var queued = 0;
    for (var i = 0; i < batch.length; i++) {
      var msg = batch[i].message.toLowerCase();
      if (msg.indexOf("update") !== -1 || msg.indexOf("available") !== -1) updates++;
      else if (msg.indexOf("queue") !== -1 || msg.indexOf("added") !== -1) queued++;
      else updates++;
    }
    var parts = [];
    if (updates > 0) parts.push(updates + " update" + (updates === 1 ? "" : "s") + " detected");
    if (queued > 0) parts.push(queued + " queued for approval");
    _showToastImmediate(parts.join(", "), "info");
  }
  function escapeHTML(str) {
    var div = document.createElement("div");
    div.appendChild(document.createTextNode(str));
    return div.innerHTML;
  }
  function showConfirm(title, bodyHTML) {
    return new Promise(function(resolve) {
      var triggerEl = document.activeElement;
      var overlay = document.createElement("div");
      overlay.className = "confirm-overlay";
      var modal = document.createElement("div");
      modal.className = "confirm-modal";
      modal.setAttribute("role", "dialog");
      modal.setAttribute("aria-modal", "true");
      var titleId = "confirm-title-" + Date.now();
      modal.setAttribute("aria-labelledby", titleId);
      var titleEl = document.createElement("div");
      titleEl.className = "confirm-title";
      titleEl.id = titleId;
      titleEl.textContent = title;
      modal.appendChild(titleEl);
      var body = document.createElement("div");
      body.className = "confirm-body";
      body.innerHTML = bodyHTML;
      modal.appendChild(body);
      var buttons = document.createElement("div");
      buttons.className = "confirm-buttons";
      var cancelBtn = document.createElement("button");
      cancelBtn.className = "confirm-btn-cancel";
      cancelBtn.textContent = "Cancel";
      cancelBtn.type = "button";
      buttons.appendChild(cancelBtn);
      var applyBtn = document.createElement("button");
      applyBtn.className = "confirm-btn-apply";
      applyBtn.textContent = "Apply";
      applyBtn.type = "button";
      buttons.appendChild(applyBtn);
      modal.appendChild(buttons);
      overlay.appendChild(modal);
      document.body.appendChild(overlay);
      cancelBtn.focus();
      function cleanup(result) {
        document.body.removeChild(overlay);
        if (triggerEl && triggerEl.focus) triggerEl.focus();
        resolve(result);
      }
      cancelBtn.addEventListener("click", function() {
        cleanup(false);
      });
      applyBtn.addEventListener("click", function() {
        cleanup(true);
      });
      overlay.addEventListener("click", function(e) {
        if (e.target === overlay) cleanup(false);
      });
      modal.addEventListener("keydown", function(e) {
        if (e.key === "Escape") {
          e.stopPropagation();
          cleanup(false);
          return;
        }
        if (e.key === "Tab") {
          var focusables = [cancelBtn, applyBtn];
          var idx = focusables.indexOf(document.activeElement);
          if (e.shiftKey) {
            if (idx <= 0) {
              e.preventDefault();
              focusables[focusables.length - 1].focus();
            }
          } else {
            if (idx >= focusables.length - 1) {
              e.preventDefault();
              focusables[0].focus();
            }
          }
        }
      });
    });
  }
  function apiPost(url, body, successMsg, errorMsg, triggerEl, onSuccess) {
    var opts = { method: "POST" };
    if (body) {
      opts.headers = { "Content-Type": "application/json" };
      opts.body = JSON.stringify(body);
    }
    if (triggerEl) {
      triggerEl.classList.add("loading");
      triggerEl.disabled = true;
    }
    function clearLoading() {
      if (triggerEl) {
        triggerEl.classList.remove("loading");
        triggerEl.disabled = false;
      }
    }
    fetch(url, opts).then(function(resp) {
      return resp.json().then(function(data) {
        return { ok: resp.ok, data };
      });
    }).then(function(result) {
      if (result.ok) {
        showToast(result.data.message || successMsg, "success");
        if (onSuccess) onSuccess(result.data);
      } else {
        showToast(result.data.error || errorMsg, "error");
      }
      clearLoading();
    }).catch(function() {
      clearLoading();
      showToast("Network error \u2014 " + errorMsg.toLowerCase(), "error");
    });
  }

  // internal/web/static/src/js/dashboard.js
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
  function initAccordionPersistence() {
    var mode = localStorage.getItem("sentinel-sections") || "remember";
    var accordions = document.querySelectorAll("details.accordion");
    var forceOpen = accordions.length === 1;
    for (var i = 0; i < accordions.length; i++) {
      var details = accordions[i];
      if (forceOpen) {
        details.open = true;
      } else if (mode === "remember") {
        var key = getAccordionKey(details);
        if (key) {
          var saved = localStorage.getItem(key);
          if (saved === "1") details.open = true;
          else if (saved === "0") details.open = false;
        }
      } else if (mode === "collapsed") {
        details.open = false;
      } else if (mode === "expanded") {
        details.open = true;
      }
      details.addEventListener("toggle", function() {
        if (localStorage.getItem("sentinel-sections") === "remember") {
          saveAccordionState(this);
        }
      });
    }
  }
  function initPauseBanner() {
    var banner = document.getElementById("pause-banner");
    if (!banner) return;
    fetch("/api/settings").then(function(r) {
      return r.json();
    }).then(function(settings) {
      if (settings["paused"] === "true") {
        banner.style.display = "";
      }
    }).catch(function() {
    });
  }
  function resumeScanning() {
    var banner = document.getElementById("pause-banner");
    fetch("/api/settings/pause", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ paused: false })
    }).then(function(resp) {
      return resp.json().then(function(data) {
        return { ok: resp.ok, data };
      });
    }).then(function(result) {
      if (result.ok) {
        if (banner) banner.style.display = "none";
        showToast("Scanning resumed", "success");
      } else {
        showToast(result.data.error || "Failed to resume scanning", "error");
      }
    }).catch(function() {
      showToast("Network error \u2014 could not resume scanning", "error");
    });
  }
  function checkPauseState() {
    var banner = document.getElementById("pause-banner");
    if (!banner) return;
    fetch("/api/settings").then(function(r) {
      return r.json();
    }).then(function(settings) {
      banner.style.display = settings["paused"] === "true" ? "" : "none";
    }).catch(function() {
    });
  }
  var lastScanTimestamp = null;
  var lastScanTimer = null;
  function refreshLastScan() {
    var el = document.getElementById("last-scan");
    if (!el) return;
    fetch("/api/last-scan").then(function(r) {
      return r.json();
    }).then(function(data) {
      if (!data.last_scan) {
        el.textContent = "Last scan: never";
        lastScanTimestamp = null;
        return;
      }
      lastScanTimestamp = new Date(data.last_scan);
      el.title = lastScanTimestamp.toLocaleString();
      renderLastScanTicker();
      if (!lastScanTimer) {
        lastScanTimer = setInterval(renderLastScanTicker, 1e3);
      }
    }).catch(function() {
    });
  }
  function renderLastScanTicker() {
    var el = document.getElementById("last-scan");
    if (!el || !lastScanTimestamp) return;
    var diff = Math.floor((Date.now() - lastScanTimestamp.getTime()) / 1e3);
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
    if (href) {
      window.location.href = href;
      return;
    }
    var host = row ? row.getAttribute("data-host") : "";
    var url = "/container/" + encodeURIComponent(name);
    if (host) url += "?host=" + encodeURIComponent(host);
    window.location.href = url;
  }
  function toggleStack(headerRow) {
    var group = headerRow.closest(".stack-group");
    if (group) {
      group.classList.toggle("stack-collapsed");
      var expanded = !group.classList.contains("stack-collapsed");
      headerRow.setAttribute("aria-expanded", expanded ? "true" : "false");
      return;
    }
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
      icon.textContent = isCollapsed ? "\u25B8" : "\u25BE";
    }
  }
  function expandAllStacks() {
    var groups = document.querySelectorAll(".stack-group");
    for (var i = 0; i < groups.length; i++) {
      groups[i].classList.remove("stack-collapsed");
      var header = groups[i].querySelector(".stack-header");
      if (header) header.setAttribute("aria-expanded", "true");
    }
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
    var hostGroups = document.querySelectorAll(".host-group");
    for (var i = 0; i < hostGroups.length; i++) {
      hostGroups[i].classList.remove("host-collapsed");
      var icon = hostGroups[i].querySelector(".expand-icon");
      if (icon) icon.textContent = "\u25BE";
    }
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
    var hostGroups = document.querySelectorAll(".host-group");
    for (var i = 0; i < hostGroups.length; i++) {
      hostGroups[i].classList.add("host-collapsed");
      var icon = hostGroups[i].querySelector(".expand-icon");
      if (icon) icon.textContent = "\u25B8";
    }
    var svcGroups = document.querySelectorAll(".svc-group");
    for (var i = 0; i < svcGroups.length; i++) {
      svcGroups[i].classList.add("svc-collapsed");
    }
  }
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
  var filterState = { status: "all", updates: "all", sort: "default" };
  var activeDashboardTab = null;
  function initFilters() {
    var saved = localStorage.getItem("sentinel-filters");
    if (saved) {
      try {
        filterState = JSON.parse(saved);
      } catch (e) {
      }
    }
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
  function activateFilter(key, value) {
    filterState = { status: "all", updates: "all", sort: "default" };
    filterState[key] = value;
    localStorage.setItem("sentinel-filters", JSON.stringify(filterState));
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
    var activeTab = activeDashboardTab;
    var stacks = table.querySelectorAll("tbody.stack-group");
    for (var s = 0; s < stacks.length; s++) {
      var stack = stacks[s];
      var stackTab = stack.getAttribute("data-tab");
      if (activeTab !== null && activeTab !== "all" && stackTab !== null && stackTab !== activeTab) continue;
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
    var svcGroups = table.querySelectorAll("tbody.svc-group");
    for (var g = 0; g < svcGroups.length; g++) {
      var svcGroup = svcGroups[g];
      var svcTab = svcGroup.getAttribute("data-tab");
      if (activeTab !== null && activeTab !== "all" && svcTab !== null && svcTab !== activeTab) continue;
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
  function initDashboardTabs() {
    var tabsEl = document.getElementById("dashboard-tabs");
    if (!tabsEl) return;
    var saved = localStorage.getItem("sentinel-dashboard-tab") || "all";
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
    if (activeBtn) {
      var total = activeBtn.getAttribute("data-stats-total") || "0";
      var running = activeBtn.getAttribute("data-stats-running") || "0";
      var pending = activeBtn.getAttribute("data-stats-pending") || "0";
      updateStats(parseInt(total, 10), parseInt(running, 10), parseInt(pending, 10));
    }
    var table = document.getElementById("container-table");
    if (table) {
      var tbodies = table.querySelectorAll("tbody");
      for (var t = 0; t < tbodies.length; t++) {
        var tb = tbodies[t];
        var tbTab = tb.getAttribute("data-tab");
        if (tbTab === null) {
          continue;
        }
        tb.style.display = tabId === "all" || tbTab === tabId ? "" : "none";
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
      btn.setAttribute("data-stats-total", String(total));
      btn.setAttribute("data-stats-running", String(running));
      btn.setAttribute("data-stats-pending", String(pending));
      if (tabId === activeDashboardTab) {
        updateStats(total, running, pending);
      }
      if (tabId !== "all") {
        var badge = btn.querySelector(".tab-badge");
        if (badge) badge.textContent = String(total);
      }
    }
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
  var updateStats = function() {
  };
  function setUpdateStatsFn(fn) {
    updateStats = fn;
  }
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
    var groups = table.querySelectorAll(".stack-group");
    for (var i = 0; i < groups.length; i++) {
      if (manageMode) {
        groups[i].setAttribute("draggable", "true");
      } else {
        groups[i].removeAttribute("draggable");
      }
    }
  }
  (function() {
    var dragSrc = null;
    document.addEventListener("dragstart", function(e) {
      var group = e.target.closest(".stack-group");
      if (!group || !manageMode) return;
      if (!e.target.closest(".stack-drag-handle")) {
        e.preventDefault();
        return;
      }
      dragSrc = group;
      group.classList.add("dragging");
      e.dataTransfer.effectAllowed = "move";
      e.dataTransfer.setData("text/plain", group.getAttribute("data-stack"));
    });
    document.addEventListener("dragover", function(e) {
      var group = e.target.closest(".stack-group");
      if (!group || !dragSrc || group === dragSrc) return;
      e.preventDefault();
      e.dataTransfer.dropEffect = "move";
      var rect = group.getBoundingClientRect();
      var mid = rect.top + rect.height / 2;
      clearDragClasses(group);
      if (e.clientY < mid) {
        group.classList.add("drag-over-above");
      } else {
        group.classList.add("drag-over-below");
      }
    });
    document.addEventListener("dragleave", function(e) {
      var group = e.target.closest(".stack-group");
      if (group) clearDragClasses(group);
    });
    document.addEventListener("drop", function(e) {
      var group = e.target.closest(".stack-group");
      if (!group || !dragSrc || group === dragSrc) return;
      e.preventDefault();
      var rect = group.getBoundingClientRect();
      var mid = rect.top + rect.height / 2;
      var table = group.closest("table");
      if (e.clientY < mid) {
        table.insertBefore(dragSrc, group);
      } else {
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
    document.addEventListener("dragend", function() {
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
        body: JSON.stringify({ order })
      }).then(function(res) {
        if (!res.ok) throw new Error("save failed");
        showToast("Stack order saved", "success");
      }).catch(function() {
        showToast("Failed to save stack order", "error");
      });
    }
  })();
  async function fetchContainerLogs(name, hostId) {
    var linesEl = document.getElementById("log-lines");
    var lines = linesEl ? linesEl.value : "50";
    var logsEl = document.getElementById("container-logs");
    if (!logsEl) return;
    logsEl.textContent = "Loading logs...";
    var url = "/api/containers/" + encodeURIComponent(name) + "/logs?lines=" + lines;
    if (hostId) url += "&host=" + encodeURIComponent(hostId);
    try {
      var resp = await fetch(url);
      if (!resp.ok) throw new Error("HTTP " + resp.status);
      var data = await resp.json();
      logsEl.textContent = data.logs || "No log output.";
      logsEl.scrollTop = logsEl.scrollHeight;
    } catch (err) {
      logsEl.textContent = "Error loading logs: " + err.message;
    }
  }

  // internal/web/static/src/js/queue.js
  function escapeHtml(str) {
    if (!str) return "";
    return str.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;").replace(/'/g, "&#039;");
  }
  function _updateQueueBadge() {
    if (window.updateQueueBadge) window.updateQueueBadge();
  }
  function _getSelectedContainers() {
    return window._dashboardSelectedContainers || {};
  }
  function _getManageMode() {
    return window._dashboardManageMode || false;
  }
  function removeQueueRow(btn) {
    var row = btn ? btn.closest("tr") : null;
    if (!row) return;
    var accordion = row.nextElementSibling;
    var hasAccordion = accordion && accordion.classList.contains("accordion-panel");
    row.style.transition = "opacity 0.15s ease";
    row.style.opacity = "0";
    if (hasAccordion) {
      accordion.style.transition = "opacity 0.15s ease";
      accordion.style.opacity = "0";
    }
    setTimeout(function() {
      if (hasAccordion) accordion.remove();
      row.remove();
      var tbody = document.querySelector(".table-wrap tbody");
      var remaining = tbody ? tbody.querySelectorAll("tr.container-row").length : 0;
      var heading = document.querySelector(".card-header h2");
      if (heading) heading.textContent = "Pending Updates (" + remaining + ")";
      _updateQueueBadge();
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
      function() {
        removeQueueRow(btn);
      }
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
      function() {
        removeQueueRow(btn);
      }
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
      function() {
        removeQueueRow(btn);
      }
    );
  }
  var _bulkInProgress = false;
  function bulkQueueAction(apiPath, actionLabel, triggerBtn) {
    var rows = document.querySelectorAll(".table-wrap tbody tr.container-row[data-queue-key]");
    if (!rows.length) return;
    _bulkInProgress = true;
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
      if (triggerBtn) {
        triggerBtn.classList.remove("loading");
        triggerBtn.disabled = false;
      }
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
      setTimeout(function() {
        window.location.reload();
      }, 400);
    }
    for (var k = 0; k < keys.length; k++) {
      (function(queueKey, delay) {
        setTimeout(function() {
          fetch("/api/" + apiPath + "/" + encodeURIComponent(queueKey), {
            method: "POST",
            credentials: "same-origin"
          }).then(function(r) {
            return r.json();
          }).then(function(data) {
            if (data.error) {
              failed++;
            } else {
              completed++;
            }
          }).catch(function() {
            failed++;
          }).then(onDone);
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
      }, 12e4);
    }
    apiPost(
      url,
      null,
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
    fetch("/api/scan", opts).then(function(resp) {
      return resp.json().then(function(data) {
        return { ok: resp.ok, data };
      });
    }).then(function(result) {
      if (!result.ok) {
        showToast(result.data.error || "Failed to trigger scan", "error");
        if (btn) {
          btn.classList.remove("loading");
          btn.disabled = false;
        }
      }
    }).catch(function() {
      showToast("Network error \u2014 failed to trigger scan", "error");
      if (btn) {
        btn.classList.remove("loading");
        btn.disabled = false;
      }
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
    }).then(function(r) {
      return r.json();
    }).then(function(data) {
      if (data.error) {
        showToast(data.error, "error");
      } else {
        showToast("Switching " + name + " to GHCR image...", "success");
      }
    }).catch(function() {
      showToast("Failed to switch to GHCR", "error");
    });
  }
  function loadAllTags(summaryEl) {
    var details = summaryEl.parentElement;
    if (details.dataset.tagsLoaded) return;
    details.dataset.tagsLoaded = "1";
    var name = window._containerName;
    if (!name) return;
    var body = document.getElementById("all-tags-body");
    var preview = document.getElementById("all-tags-preview");
    fetch("/api/containers/" + encodeURIComponent(name) + "/tags").then(function(r) {
      return r.json();
    }).then(function(tags) {
      if (!tags || tags.length === 0) {
        if (body) body.innerHTML = '<div class="accordion-empty">No tags found.</div>';
        if (preview) preview.textContent = "None";
        return;
      }
      if (preview) preview.textContent = tags.length + " tags";
      var html = '<div class="tag-list">';
      for (var i = 0; i < tags.length; i++) {
        html += '<span class="badge badge-muted tag-item">' + escapeHtml(tags[i]) + "</span>";
      }
      html += "</div>";
      if (body) body.innerHTML = html;
    }).catch(function() {
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
    showConfirm(
      "Update to Version",
      "<p>Update <strong>" + name + "</strong> to <code>" + tag + "</code>?</p>"
    ).then(function(confirmed) {
      if (!confirmed) return;
      fetch(url, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ tag })
      }).then(function(r) {
        return r.json();
      }).then(function(data) {
        if (data.error) {
          showToast(data.error, "error");
        } else {
          showToast(data.message || "Update started", "success");
        }
      }).catch(function() {
        showToast("Failed to trigger version update", "error");
      });
    });
  }
  function applyBulkPolicy() {
    var selectedContainers2 = _getSelectedContainers();
    var names = [];
    var keys = Object.keys(selectedContainers2);
    for (var i = 0; i < keys.length; i++) {
      if (selectedContainers2[keys[i]]) names.push(keys[i]);
    }
    if (names.length === 0) return;
    var policyEl = document.getElementById("bulk-policy");
    var policy = policyEl ? policyEl.value : "manual";
    fetch("/api/bulk/policy", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ containers: names, policy })
    }).then(function(resp) {
      return resp.json().then(function(data) {
        return { ok: resp.ok, data };
      });
    }).then(function(result) {
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
      var bodyHTML = "";
      for (var i2 = 0; i2 < preview.changes.length; i2++) {
        var c = preview.changes[i2];
        bodyHTML += '<div class="confirm-change-row"><span class="confirm-change-name">' + escapeHTML(c.name) + '</span> <span class="badge badge-muted">' + escapeHTML(c.from) + '</span> \u2192 <span class="badge badge-info">' + escapeHTML(c.to) + "</span></div>";
      }
      if (blockedCount > 0) {
        bodyHTML += '<div class="confirm-section-label">Blocked (' + blockedCount + ")</div>";
        for (var b = 0; b < preview.blocked.length; b++) {
          bodyHTML += '<div class="confirm-muted-row">' + escapeHTML(preview.blocked[b].name) + " \u2014 " + escapeHTML(preview.blocked[b].reason) + "</div>";
        }
      }
      if (unchangedCount > 0) {
        bodyHTML += '<div class="confirm-section-label">Unchanged (' + unchangedCount + ")</div>";
        for (var u = 0; u < preview.unchanged.length; u++) {
          bodyHTML += '<div class="confirm-muted-row">' + escapeHTML(preview.unchanged[u].name) + " \u2014 " + escapeHTML(preview.unchanged[u].reason) + "</div>";
        }
      }
      var confirmTitle = "Change policy to \u2018" + escapeHTML(policy) + "\u2019 for " + changeCount + " container" + (changeCount !== 1 ? "s" : "") + "?";
      showConfirm(confirmTitle, bodyHTML).then(function(confirmed) {
        if (!confirmed) return;
        fetch("/api/bulk/policy", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ containers: names, policy, confirm: true })
        }).then(function(resp2) {
          return resp2.json().then(function(data2) {
            return { ok: resp2.ok, data: data2 };
          });
        }).then(function(confirmResult) {
          if (!confirmResult.ok) {
            showToast(confirmResult.data.error || "Failed to apply bulk policy change", "error");
            return;
          }
          var applied = confirmResult.data.applied || 0;
          showToast("Policy changed to '" + policy + "' for " + applied + " container" + (applied !== 1 ? "s" : ""), "success");
          for (var r = 0; r < preview.changes.length; r++) {
            if (window.updateContainerRow) window.updateContainerRow(preview.changes[r].name);
          }
          if (window.clearSelection) window.clearSelection();
          if (_getManageMode() && window.toggleManageMode) window.toggleManageMode();
        }).catch(function() {
          showToast("Network error \u2014 could not apply bulk policy change", "error");
        });
      });
    }).catch(function() {
      showToast("Network error \u2014 could not preview bulk policy change", "error");
    });
  }
  function getBulkInProgress() {
    return _bulkInProgress;
  }

  // internal/web/static/src/js/swarm.js
  function escapeHtml2(str) {
    if (!str) return "";
    return str.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;").replace(/'/g, "&#039;");
  }
  function isSafeURL(url) {
    return typeof url === "string" && (url.indexOf("https://") === 0 || url.indexOf("http://") === 0);
  }
  function showBadgeSpinner(wrap) {
    wrap.setAttribute("data-pending", "");
  }
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
      setTimeout(function() {
        if (window._svcLoadingBtns && window._svcLoadingBtns[name]) {
          window._svcLoadingBtns[name].classList.remove("loading");
          window._svcLoadingBtns[name].disabled = false;
          delete window._svcLoadingBtns[name];
        }
      }, 6e4);
    }
    apiPost(
      "/api/services/" + encodeURIComponent(name) + "/update",
      null,
      "Service update started for " + name,
      "Failed to trigger service update"
    );
    var delays = [2e3, 5e3, 1e4, 2e4];
    for (var i = 0; i < delays.length; i++) {
      (function(d) {
        setTimeout(function() {
          refreshServiceRow(name);
        }, d);
      })(delays[i]);
    }
  }
  function changeSvcPolicy(name, newPolicy) {
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
      }, 6e4);
    }
    apiPost(
      "/api/services/" + encodeURIComponent(name) + "/rollback",
      null,
      "Rollback started for " + name,
      "Failed to rollback " + name
    );
    var delays = [2e3, 5e3, 1e4, 2e4];
    for (var i = 0; i < delays.length; i++) {
      (function(d) {
        setTimeout(function() {
          refreshServiceRow(name);
        }, d);
      })(delays[i]);
    }
  }
  var _svcTaskCache = {};
  function scaleSvc(name, replicas, wrap) {
    if (wrap) showBadgeSpinner(wrap);
    if (replicas === 0) {
      var group = document.querySelector('.svc-group[data-service="' + name + '"]');
      if (group) {
        var taskRows = group.querySelectorAll(".svc-task-row");
        var cached = [];
        for (var t = 0; t < taskRows.length; t++) {
          var nodeCell = taskRows[t].querySelector(".svc-node");
          var tagCell = taskRows[t].querySelector(".mono");
          cached.push({
            NodeText: nodeCell ? nodeCell.textContent : "",
            Tag: tagCell ? tagCell.textContent : ""
          });
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
      delete _svcTaskCache[name];
    }
    apiPost(
      "/api/services/" + encodeURIComponent(name) + "/scale",
      { replicas },
      "Scaled " + name + " to " + replicas + " replicas",
      "Failed to scale " + name
    );
    var delays = [2e3, 5e3, 1e4, 2e4];
    for (var i = 0; i < delays.length; i++) {
      (function(d) {
        setTimeout(function() {
          refreshServiceRow(name);
        }, d);
      })(delays[i]);
    }
  }
  function refreshServiceRow(name) {
    fetch("/api/services/" + encodeURIComponent(name) + "/detail").then(function(r) {
      return r.json();
    }).then(function(svc) {
      var group = document.querySelector('.svc-group[data-service="' + name + '"]');
      if (!group) return;
      var header = group.querySelector(".svc-header");
      var wrap = group.querySelector(".status-badge-wrap[data-service]");
      if (wrap) {
        wrap.style.pointerEvents = "";
        wrap.removeAttribute("data-pending");
        var prevReplicas = svc.PrevReplicas || parseInt(wrap.getAttribute("data-prev-replicas"), 10) || 1;
        if (svc.DesiredReplicas > 0) {
          var replicaClass = svc.RunningReplicas === svc.DesiredReplicas ? "svc-replicas-healthy" : svc.RunningReplicas > 0 ? "svc-replicas-degraded" : "svc-replicas-down";
          wrap.setAttribute("data-prev-replicas", svc.DesiredReplicas);
          wrap.innerHTML = '<span class="badge svc-replicas ' + replicaClass + ' badge-default">' + escapeHtml2(svc.Replicas || "") + `</span><span class="badge badge-error badge-hover" onclick="event.stopPropagation(); scaleSvc('` + escapeHtml2(name) + `', 0, this.closest('.status-badge-wrap'))">Scale to 0</span>`;
        } else {
          wrap.setAttribute("data-prev-replicas", prevReplicas);
          wrap.innerHTML = '<span class="badge svc-replicas svc-replicas-down badge-default">' + escapeHtml2(svc.Replicas || "0/0") + `</span><span class="badge badge-success badge-hover" onclick="event.stopPropagation(); scaleSvc('` + escapeHtml2(name) + "', " + (prevReplicas > 0 ? prevReplicas : 1) + `, this.closest('.status-badge-wrap'))">Scale up</span>`;
        }
      }
      if (header) {
        var imgCell = header.querySelector(".cell-image");
        if (imgCell && svc.Tag) {
          var oldBadge = imgCell.querySelector(".registry-badge");
          if (oldBadge) oldBadge.remove();
          var rvSpan = svc.ResolvedVersion ? ' <span class="resolved-ver">(' + escapeHtml2(svc.ResolvedVersion) + ")</span>" : "";
          if (svc.NewestVersion) {
            var verHtml = escapeHtml2(svc.NewestVersion);
            if (svc.VersionURL && isSafeURL(svc.VersionURL)) {
              verHtml = '<a href="' + escapeHtml2(svc.VersionURL) + '" target="_blank" rel="noopener" class="version-new version-link">' + escapeHtml2(svc.NewestVersion) + "</a>";
            } else {
              verHtml = '<span class="version-new">' + verHtml + "</span>";
            }
            imgCell.innerHTML = '<span class="version-current">' + escapeHtml2(svc.Tag) + rvSpan + '</span> <span class="version-arrow">&rarr;</span> ' + verHtml;
          } else {
            var tagHtml = escapeHtml2(svc.Tag) + rvSpan;
            if (svc.ChangelogURL && isSafeURL(svc.ChangelogURL)) {
              imgCell.innerHTML = '<a href="' + escapeHtml2(svc.ChangelogURL) + '" target="_blank" rel="noopener" class="version-link">' + tagHtml + "</a>";
            } else {
              imgCell.innerHTML = tagHtml;
            }
          }
          imgCell.setAttribute("title", svc.Image || "");
          _applyRegistryBadges();
        }
        if (svc.HasUpdate) {
          header.classList.add("has-update");
        } else {
          header.classList.remove("has-update");
        }
        var actionCell = header.querySelector("td:last-child .btn-group");
        if (actionCell) {
          var isUpdating = window._svcLoadingBtns && window._svcLoadingBtns[name];
          var btns = "";
          if (svc.HasUpdate && svc.Policy !== "pinned") {
            btns += '<button class="btn btn-warning btn-sm' + (isUpdating ? " loading" : "") + '"' + (isUpdating ? " disabled" : "") + ` onclick="event.stopPropagation(); triggerSvcUpdate('` + escapeHtml2(name) + `', event)">Update</button>`;
          }
          btns += '<a href="/service/' + encodeURIComponent(name) + '" class="btn btn-sm" onclick="event.stopPropagation()">Details</a>';
          actionCell.innerHTML = btns;
          if (isUpdating) {
            var newBtn = actionCell.querySelector(".btn-warning");
            if (newBtn) window._svcLoadingBtns[name] = newBtn;
          }
        }
      }
      if (window._svcLoadingBtns) {
        var b = window._svcLoadingBtns[name];
        if (b && !b.isConnected) {
          delete window._svcLoadingBtns[name];
        }
      }
      var taskRows = group.querySelectorAll(".svc-task-row");
      for (var t = taskRows.length - 1; t >= 0; t--) {
        taskRows[t].remove();
      }
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
            stateBadge = '<span class="badge badge-error" title="' + escapeHtml2(task.Error || "") + '">' + escapeHtml2(task.State) + "</span>";
          }
          var nodeDisplay = escapeHtml2(task.NodeName);
          if (task.NodeAddr) {
            nodeDisplay += ' <span class="svc-node-addr">(' + escapeHtml2(task.NodeAddr) + ")</span>";
          }
          tr.innerHTML = '<td></td><td class="svc-node">' + nodeDisplay + '</td><td class="mono">' + escapeHtml2(task.Tag || "") + "</td><td></td><td>" + stateBadge + "</td><td></td>";
          taskHeader.parentNode.insertBefore(tr, taskHeader.nextSibling);
        }
      } else if (taskHeader && svc.DesiredReplicas === 0) {
        var cached = _svcTaskCache[name];
        if (cached && cached.length > 0) {
          for (var t = cached.length - 1; t >= 0; t--) {
            var tr = document.createElement("tr");
            tr.className = "svc-task-row";
            tr.innerHTML = '<td></td><td class="svc-node">' + escapeHtml2(cached[t].NodeText || "") + '</td><td class="mono">' + escapeHtml2(cached[t].Tag || "") + '</td><td></td><td><span class="badge badge-error">shutdown</span></td><td></td>';
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
      setTimeout(function() {
        group.classList.remove("row-updated");
      }, 300);
    }).catch(function() {
    });
  }

  // internal/web/static/src/js/sse.js
  var ghcrAlternatives = {};
  var sseReloadTimer = null;
  function scheduleReload() {
    if (!document.getElementById("container-table")) return;
    if (sseReloadTimer) clearTimeout(sseReloadTimer);
    sseReloadTimer = setTimeout(function() {
      window.location.reload();
    }, 800);
  }
  var _queueReloadTimer = null;
  function scheduleQueueReload() {
    if (_queueReloadTimer) clearTimeout(_queueReloadTimer);
    _queueReloadTimer = setTimeout(function() {
      _queueReloadTimer = null;
      window.location.reload();
    }, 2e3);
  }
  function updateContainerRow(name, hostId) {
    var enc = encodeURIComponent(name);
    var url = "/api/containers/" + enc + "/row";
    if (hostId) url += "?host=" + encodeURIComponent(hostId);
    fetch(url).then(function(r) {
      return r.json();
    }).then(function(data) {
      if (!data.html) return;
      updateStats2(data.total, data.running, data.pending);
      var temp = document.createElement("tbody");
      temp.innerHTML = data.html;
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
        var newCb = newRow ? newRow.querySelector(".row-select") : null;
        var _selCont = window._dashboardSelectedContainers || {};
        if (newCb && _selCont[newCb.value]) {
          newCb.checked = true;
        }
        if (window.recomputeSelectionState) window.recomputeSelectionState();
      }
      applyRegistryBadges();
      if (window.applyFiltersAndSort) window.applyFiltersAndSort();
      if (window.recalcTabStats) window.recalcTabStats();
      clearPendingBadge(name, hostId);
      reapplyBadgeSpinners();
      if (window._updateLoadingBtns) {
        var updKey = (hostId || "") + "::" + name;
        if (window._updateLoadingBtns[updKey]) {
          var sel = 'tr.container-row[data-name="' + name + '"]';
          if (hostId) sel += '[data-host="' + hostId + '"]';
          var row = document.querySelector(sel);
          var updBtn = row ? row.querySelector(".btn-warning.loading") : null;
          if (updBtn) {
            window._updateLoadingBtns[updKey] = updBtn;
          } else {
            delete window._updateLoadingBtns[updKey];
          }
        }
      }
    }).catch(function() {
      scheduleReload();
    });
  }
  function updateStats2(total, running, pending) {
    var stats = document.getElementById("stats");
    if (!stats) return;
    var values = stats.querySelectorAll(".stat-value");
    var newVals = [total, running, pending];
    for (var i = 0; i < values.length && i < newVals.length; i++) {
      if (values[i] && String(values[i].textContent).trim() !== String(newVals[i])) {
        values[i].textContent = newVals[i];
        values[i].classList.remove("stat-changed");
        void values[i].offsetWidth;
        values[i].classList.add("stat-changed");
      }
    }
    updatePendingColor(pending);
  }
  function refreshDashboardStats() {
    if (!document.getElementById("stats")) return;
    fetch("/api/stats", { credentials: "same-origin" }).then(function(r) {
      return r.json();
    }).then(function(data) {
      updateStats2(data.total, data.running, data.pending);
    }).catch(function() {
    });
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
  function showBadgeSpinner2(wrap) {
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
    es.addEventListener("connected", function() {
      if (localStorage.getItem("sentinel-self-updating")) {
        localStorage.removeItem("sentinel-self-updating");
        window.location.reload();
        return;
      }
      setConnectionStatus(true);
    });
    es.addEventListener("container_update", function(e) {
      try {
        var data = JSON.parse(e.data);
        var toastType = "info";
        if (data.message) {
          if (data.message.indexOf("failed") !== -1) toastType = "error";
          else if (data.message.indexOf("success") !== -1) toastType = "success";
        }
        queueBatchToast(data.message || "Update: " + data.container_name, toastType);
        if (data.container_name) {
          updateContainerRow(data.container_name, data.host_id);
          return;
        }
      } catch (_) {
      }
      scheduleReload();
    });
    es.addEventListener("container_state", function(e) {
      try {
        var data = JSON.parse(e.data);
        if (data.container_name) {
          updateContainerRow(data.container_name, data.host_id);
          return;
        }
      } catch (_) {
      }
      scheduleReload();
    });
    es.addEventListener("queue_change", function(e) {
      var data;
      try {
        data = JSON.parse(e.data);
        queueBatchToast(data.message || "Queue updated", "info");
      } catch (_) {
        data = {};
      }
      if (document.getElementById("container-table")) {
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
    es.addEventListener("scan_complete", function(e) {
      var scanBtn = document.getElementById("scan-btn");
      if (scanBtn) {
        scanBtn.classList.remove("loading");
        scanBtn.disabled = false;
      }
      if (window.checkPauseState) window.checkPauseState();
      if (window.refreshLastScan) window.refreshLastScan();
      if (document.getElementById("container-table")) {
        refreshDashboardStats();
        if (window.recalcTabStats) window.recalcTabStats();
        scheduleDigestBannerRefresh();
      } else {
        scheduleReload();
      }
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
    es.addEventListener("rate_limits", function(e) {
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
      } catch (_) {
      }
    });
    es.addEventListener("settings_change", function() {
      if (window.checkPauseState) window.checkPauseState();
    });
    es.addEventListener("policy_change", function(e) {
      try {
        var data = JSON.parse(e.data);
        showToast(data.message || "Policy changed: " + data.container_name, "info");
        if (data.container_name) {
          updateContainerRow(data.container_name, data.host_id);
          return;
        }
      } catch (_) {
      }
      scheduleReload();
    });
    es.addEventListener("digest_ready", function(e) {
      try {
        var data = JSON.parse(e.data);
        showToast(data.message || "Digest ready", "info");
      } catch (_) {
      }
      loadDigestBanner();
    });
    es.addEventListener("service_update", function(e) {
      try {
        var data = JSON.parse(e.data);
        queueBatchToast(data.message || "Service: " + data.container_name, "info");
        if (data.container_name) {
          if (window.refreshServiceRow) window.refreshServiceRow(data.container_name);
          setTimeout(function() {
            if (window.refreshServiceRow) window.refreshServiceRow(data.container_name);
          }, 1e4);
        }
      } catch (_) {
      }
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
          link.onclick = function(ev) {
            ev.stopPropagation();
          };
          var count = inner.querySelector(".host-count");
          inner.insertBefore(link, count);
        } else if (!offline && existing) {
          existing.remove();
        }
      } catch (_) {
      }
    });
    es.onopen = function() {
      setConnectionStatus(true);
    };
    es.onerror = function() {
      setConnectionStatus(false);
    };
  }
  function loadGHCRAlternatives() {
    fetch("/api/ghcr/alternatives").then(function(r) {
      return r.json();
    }).then(function(data) {
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
    }).catch(function() {
    });
  }
  var registryStyles = {
    "docker.io": { label: "Hub", cls: "registry-badge-hub" },
    "ghcr.io": { label: "GHCR", cls: "registry-badge-ghcr" },
    "lscr.io": { label: "LSCR", cls: "registry-badge-lscr" },
    "docker.gitea.com": { label: "Gitea", cls: "registry-badge-gitea" }
  };
  function applyRegistryBadges() {
    var rows = document.querySelectorAll("tr.container-row, tr.svc-header");
    rows.forEach(function(row) {
      var imageCell = row.querySelector(".cell-image");
      if (!imageCell) return;
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
      var existing = row.querySelector(".ghcr-badge");
      if (existing) existing.remove();
      var imageCell = row.querySelector(".cell-image");
      if (!imageCell) return;
      var title = imageCell.getAttribute("title") || "";
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
    var ref = imageRef.split("@")[0].split(":")[0];
    ref = ref.replace(/^docker\.io\//, "");
    var firstSegment = ref.split("/")[0];
    if (firstSegment.indexOf(".") !== -1) return null;
    if (ref.indexOf("/") === -1) return null;
    if (ref.indexOf("library/") === 0) return null;
    return ref;
  }
  function renderGHCRAlternatives() {
    var container = document.getElementById("ghcr-alternatives-table");
    if (!container) return;
    fetch("/api/ghcr/alternatives").then(function(r) {
      return r.json();
    }).then(function(data) {
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
    }).catch(function() {
      while (container.firstChild) container.removeChild(container.firstChild);
      var errP = document.createElement("p");
      errP.className = "text-muted";
      errP.textContent = "Failed to load GHCR alternatives.";
      container.appendChild(errP);
    });
  }
  function loadDigestBanner() {
    var banner = document.getElementById("digest-banner");
    if (!banner) return;
    fetch("/api/digest/banner", { credentials: "same-origin" }).then(function(res) {
      return res.json();
    }).then(function(data) {
      if (data.count > 0) {
        var text = "Pending updates: " + data.pending.join(", ") + " (" + data.count + " container" + (data.count === 1 ? "" : "s") + " awaiting action)";
        document.getElementById("digest-banner-text").textContent = text;
        banner.style.display = "";
        banner.classList.remove("banner-hidden");
      } else {
        banner.classList.add("banner-hidden");
      }
    }).catch(function() {
      banner.classList.add("banner-hidden");
    });
  }
  var _digestBannerTimer = null;
  function scheduleDigestBannerRefresh() {
    if (_digestBannerTimer) clearTimeout(_digestBannerTimer);
    _digestBannerTimer = setTimeout(function() {
      _digestBannerTimer = null;
      loadDigestBanner();
    }, 1500);
  }
  var _queueBadgeTimer = null;
  function updateQueueBadge() {
    if (_queueBadgeTimer) clearTimeout(_queueBadgeTimer);
    _queueBadgeTimer = setTimeout(function() {
      _queueBadgeTimer = null;
      fetch("/api/queue/count", { credentials: "same-origin" }).then(function(r) {
        return r.json();
      }).then(function(data) {
        var badges = document.querySelectorAll(".nav-badge");
        var count = typeof data.count === "number" ? data.count : 0;
        for (var i = 0; i < badges.length; i++) {
          var link = badges[i].closest("a");
          if (link && link.href && link.href.indexOf("/queue") !== -1) {
            badges[i].textContent = count;
            badges[i].style.display = count > 0 ? "" : "none";
          }
        }
      }).catch(function() {
      });
    }, 300);
  }

  // internal/web/static/src/js/settings-core.js
  function initSettingsPage() {
    var themeSelect = document.getElementById("theme-select");
    var stackSelect = document.getElementById("stack-default");
    var sectionSelect = document.getElementById("section-default");
    if (!themeSelect) return;
    themeSelect.value = localStorage.getItem("sentinel-theme") || "auto";
    stackSelect.value = localStorage.getItem("sentinel-stacks") || "collapsed";
    if (sectionSelect) sectionSelect.value = localStorage.getItem("sentinel-sections") || "remember";
    themeSelect.addEventListener("change", function() {
      if (window.applyTheme) window.applyTheme(themeSelect.value);
      localStorage.setItem("sentinel-theme", themeSelect.value);
      showToast("Theme updated", "success");
    });
    stackSelect.addEventListener("change", function() {
      localStorage.setItem("sentinel-stacks", stackSelect.value);
      showToast("Stack default updated", "success");
    });
    if (sectionSelect) {
      sectionSelect.addEventListener("change", function() {
        localStorage.setItem("sentinel-sections", sectionSelect.value);
        if (sectionSelect.value !== "remember") {
          clearAccordionState();
        }
        showToast("Section default updated", "success");
      });
    }
    fetch("/api/settings").then(function(r) {
      return r.json();
    }).then(function(settings) {
      var pollSelect = document.getElementById("poll-interval");
      if (pollSelect) {
        var current = settings["SENTINEL_POLL_INTERVAL"] || settings["poll_interval"] || "6h0m0s";
        var normalised = normaliseDuration(current);
        var matched = selectOptionByValue(pollSelect, normalised);
        if (!matched && normalised !== "") {
          pollSelect.value = "custom";
          populateCustomDuration("poll", normalised);
        }
      }
      var policySelect = document.getElementById("default-policy");
      if (policySelect) {
        var policy = settings["default_policy"] || settings["SENTINEL_DEFAULT_POLICY"] || "manual";
        selectOptionByValue(policySelect, policy);
      }
      var rbSelect = document.getElementById("rollback-policy");
      if (rbSelect) {
        var rbPolicy = settings["rollback_policy"] || settings["SENTINEL_ROLLBACK_POLICY"] || "";
        selectOptionByValue(rbSelect, rbPolicy);
      }
      var graceSelect = document.getElementById("grace-period");
      if (graceSelect) {
        var grace = settings["grace_period"] || settings["SENTINEL_GRACE_PERIOD"] || "30s";
        var graceNorm = normaliseDuration(grace);
        var graceMatched = selectOptionByValue(graceSelect, graceNorm);
        if (!graceMatched && graceNorm !== "") {
          graceSelect.value = "custom";
          populateCustomDuration("grace", graceNorm);
        }
      }
      var latestAutoToggle = document.getElementById("latest-auto-toggle");
      if (latestAutoToggle) {
        var latestAuto = settings["latest_auto_update"] !== "false";
        latestAutoToggle.checked = latestAuto;
        updateLatestAutoText(latestAuto);
      }
      var pauseToggle = document.getElementById("pause-toggle");
      if (pauseToggle) {
        var paused = settings["paused"] === "true";
        pauseToggle.checked = paused;
        updatePauseToggleText(paused);
      }
      var filtersArea = document.getElementById("container-filters");
      if (filtersArea) {
        var filters = settings["filters"] || "";
        filtersArea.value = filters;
      }
      var imageCleanupToggle = document.getElementById("image-cleanup-toggle");
      if (imageCleanupToggle) {
        var imageCleanup = settings["image_cleanup"] === "true";
        imageCleanupToggle.checked = imageCleanup;
        updateToggleText("image-cleanup-text", imageCleanup);
      }
      var cronInput = document.getElementById("cron-schedule");
      if (cronInput) {
        cronInput.value = settings["schedule"] || "";
      }
      var depAwareToggle = document.getElementById("dep-aware-toggle");
      if (depAwareToggle) {
        var depAware = settings["dependency_aware"] === "true";
        depAwareToggle.checked = depAware;
        updateToggleText("dep-aware-text", depAware);
      }
      var hooksToggle = document.getElementById("hooks-toggle");
      if (hooksToggle) {
        var hooksEnabled = settings["hooks_enabled"] === "true";
        hooksToggle.checked = hooksEnabled;
        updateToggleText("hooks-toggle-text", hooksEnabled);
      }
      var hooksLabelsToggle = document.getElementById("hooks-labels-toggle");
      if (hooksLabelsToggle) {
        var hooksLabels = settings["hooks_write_labels"] === "true";
        hooksLabelsToggle.checked = hooksLabels;
        updateToggleText("hooks-labels-text", hooksLabels);
      }
      var dryRunToggle = document.getElementById("dry-run-toggle");
      if (dryRunToggle) {
        var dryRun = settings["dry_run"] === "true";
        dryRunToggle.checked = dryRun;
        updateToggleText("dry-run-text", dryRun);
      }
      var pullOnlyToggle = document.getElementById("pull-only-toggle");
      if (pullOnlyToggle) {
        var pullOnly = settings["pull_only"] === "true";
        pullOnlyToggle.checked = pullOnly;
        updateToggleText("pull-only-text", pullOnly);
      }
      var updateDelayInput = document.getElementById("update-delay");
      if (updateDelayInput) {
        updateDelayInput.value = settings["update_delay"] || "";
      }
      var maintenanceWindowInput = document.getElementById("maintenance-window");
      if (maintenanceWindowInput) {
        maintenanceWindowInput.value = settings["maintenance_window"] || "";
      }
      var composeSyncToggle = document.getElementById("compose-sync-toggle");
      if (composeSyncToggle) {
        var composeSync = settings["compose_sync"] === "true";
        composeSyncToggle.checked = composeSync;
        updateToggleText("compose-sync-text", composeSync);
      }
      var imageBackupToggle = document.getElementById("image-backup-toggle");
      if (imageBackupToggle) {
        var imageBackup = settings["image_backup"] === "true";
        imageBackupToggle.checked = imageBackup;
        updateToggleText("image-backup-text", imageBackup);
      }
      var showStoppedToggle = document.getElementById("show-stopped-toggle");
      if (showStoppedToggle) {
        var showStopped = settings["show_stopped"] === "true";
        showStoppedToggle.checked = showStopped;
        updateToggleText("show-stopped-text", showStopped);
      }
      var removeVolumesToggle = document.getElementById("remove-volumes-toggle");
      if (removeVolumesToggle) {
        var removeVolumes = settings["remove_volumes"] === "true";
        removeVolumesToggle.checked = removeVolumes;
        updateToggleText("remove-volumes-text", removeVolumes);
      }
      var scanConcInput = document.getElementById("scan-concurrency-input");
      if (scanConcInput && settings["scan_concurrency"]) {
        var sc = parseInt(settings["scan_concurrency"], 10);
        if (!isNaN(sc) && sc >= 1) {
          scanConcInput.value = sc;
        }
      }
      var haToggle = document.getElementById("ha-discovery-toggle");
      if (haToggle) {
        var haEnabled = settings["ha_discovery_enabled"] === "true";
        haToggle.checked = haEnabled;
        updateToggleText("ha-discovery-text", haEnabled);
      }
      var haPrefixInput = document.getElementById("ha-discovery-prefix");
      if (haPrefixInput) {
        haPrefixInput.value = settings["ha_discovery_prefix"] || "homeassistant";
      }
      updateScanPreviews();
    }).catch(function() {
    });
    var tabBtns = document.querySelectorAll(".tab-btn");
    var tabPanels = document.querySelectorAll(".tab-panel");
    if (tabBtns.length > 0) {
      var savedTab = localStorage.getItem("sentinel-settings-tab");
      if (savedTab) {
        var tabExists = false;
        for (var i = 0; i < tabBtns.length; i++) {
          if (tabBtns[i].getAttribute("data-tab") === savedTab) {
            tabExists = true;
            break;
          }
        }
        if (tabExists) {
          for (var i = 0; i < tabBtns.length; i++) {
            var isTarget = tabBtns[i].getAttribute("data-tab") === savedTab;
            tabBtns[i].classList.toggle("active", isTarget);
            tabBtns[i].setAttribute("aria-selected", isTarget ? "true" : "false");
          }
          for (var i = 0; i < tabPanels.length; i++) {
            tabPanels[i].classList.toggle("active", tabPanels[i].id === "tab-" + savedTab);
          }
        }
      }
      for (var i = 0; i < tabBtns.length; i++) {
        tabBtns[i].addEventListener("click", function() {
          var tab = this.getAttribute("data-tab");
          localStorage.setItem("sentinel-settings-tab", tab);
          for (var j = 0; j < tabBtns.length; j++) {
            tabBtns[j].classList.toggle("active", tabBtns[j] === this);
            tabBtns[j].setAttribute("aria-selected", tabBtns[j] === this ? "true" : "false");
          }
          for (var j = 0; j < tabPanels.length; j++) {
            tabPanels[j].classList.toggle("active", tabPanels[j].id === "tab-" + tab);
          }
        });
      }
    }
    var securityBtn = document.querySelector('[data-tab="security"]');
    if (securityBtn && !document.querySelector(".nav-user-btn")) {
      securityBtn.click();
    }
    if (window.loadNotificationChannels) window.loadNotificationChannels();
    if (window.loadDigestSettings) window.loadDigestSettings();
    if (window.loadContainerNotifyPrefs) window.loadContainerNotifyPrefs();
    if (window.loadNotifyTemplates) window.loadNotifyTemplates();
    if (window.loadRegistries) window.loadRegistries();
    if (window.loadReleaseSources) window.loadReleaseSources();
    if (window.renderGHCRAlternatives) window.renderGHCRAlternatives();
    if (window.loadAboutInfo) window.loadAboutInfo();
    if (window.loadClusterSettings) window.loadClusterSettings();
    loadWebhookSettings();
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
  function normaliseDuration(dur) {
    return dur.replace("0m0s", "").replace("0s", "").replace(/^0h/, "");
  }
  function onCustomUnitChange(prefix) {
    var unitEl = document.getElementById(prefix + "-custom-unit");
    var valEl = document.getElementById(prefix + "-custom-value");
    var btnEl = document.getElementById(prefix + "-custom-btn");
    var hasUnit = unitEl && unitEl.value !== "";
    if (valEl) {
      valEl.disabled = !hasUnit;
      if (hasUnit) valEl.focus();
    }
    if (btnEl) btnEl.disabled = !hasUnit;
  }
  function parseDuration(dur) {
    var match = dur.match(/^(\d+)(s|m|h)$/);
    if (!match) return null;
    return { value: match[1], unit: match[2] };
  }
  function populateCustomDuration(prefix, normalised) {
    var wrap = document.getElementById(prefix + "-custom-wrap");
    var unitEl = document.getElementById(prefix + "-custom-unit");
    var valEl = document.getElementById(prefix + "-custom-value");
    var btnEl = document.getElementById(prefix + "-custom-btn");
    if (!wrap) return;
    wrap.style.display = "";
    var parsed = parseDuration(normalised);
    if (parsed && unitEl && valEl) {
      unitEl.value = parsed.unit;
      valEl.value = parsed.value;
      valEl.disabled = false;
      if (btnEl) btnEl.disabled = false;
    }
  }
  function selectOptionByValue(selectEl, value) {
    for (var i = 0; i < selectEl.options.length; i++) {
      if (selectEl.options[i].value === value) {
        selectEl.selectedIndex = i;
        return true;
      }
    }
    return false;
  }
  function updatePauseToggleText(paused) {
    var text = document.getElementById("pause-toggle-text");
    if (text) {
      text.textContent = paused ? "Paused" : "Off";
    }
  }
  function updateScanPreviews() {
    var el = document.getElementById("scan-schedule-preview");
    if (el) {
      var pauseToggle = document.getElementById("pause-toggle");
      var cronInput = document.getElementById("cron-schedule");
      var pollSelect = document.getElementById("poll-interval");
      if (pauseToggle && pauseToggle.checked) {
        el.textContent = "Paused";
      } else if (cronInput && cronInput.value) {
        el.textContent = "Cron: " + cronInput.value;
      } else if (pollSelect) {
        if (pollSelect.value === "custom") {
          var cv = (document.getElementById("poll-custom-value") || {}).value || "";
          var cu = (document.getElementById("poll-custom-unit") || {}).value || "";
          el.textContent = cv && cu ? "Every " + cv + cu : "Custom";
        } else {
          el.textContent = "Every " + pollSelect.options[pollSelect.selectedIndex].text;
        }
      }
    }
    var el2 = document.getElementById("update-policy-preview");
    if (el2) {
      var policySelect = document.getElementById("default-policy");
      var txt = "Default: " + (policySelect ? policySelect.value : "manual");
      var mwInput = document.getElementById("maintenance-window");
      if (mwInput && mwInput.value) {
        txt += ", window " + mwInput.value;
      } else {
        var graceSelect = document.getElementById("grace-period");
        if (graceSelect) {
          if (graceSelect.value === "custom") {
            var gv = (document.getElementById("grace-custom-value") || {}).value || "";
            var gu = (document.getElementById("grace-custom-unit") || {}).value || "";
            txt += gv && gu ? ", grace " + gv + gu : "";
          } else {
            txt += ", grace " + graceSelect.options[graceSelect.selectedIndex].text;
          }
        }
      }
      el2.textContent = txt;
    }
  }
  function onPollIntervalChange(value) {
    var wrap = document.getElementById("poll-custom-wrap");
    if (value === "custom") {
      if (wrap) wrap.style.display = "";
      updateScanPreviews();
      return;
    }
    if (wrap) wrap.style.display = "none";
    setPollInterval(value);
    updateScanPreviews();
  }
  function applyCustomPollInterval() {
    var unit = document.getElementById("poll-custom-unit");
    var val = document.getElementById("poll-custom-value");
    if (!unit || !val || !unit.value || !val.value) return;
    setPollInterval(val.value + unit.value);
    updateScanPreviews();
  }
  function setPollInterval(interval) {
    fetch("/api/settings/poll-interval", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ interval })
    }).then(function(resp) {
      return resp.json().then(function(data) {
        return { ok: resp.ok, data };
      });
    }).then(function(result) {
      if (result.ok) {
        showToast(result.data.message || "Poll interval updated", "success");
      } else {
        showToast(result.data.error || "Failed to update poll interval", "error");
      }
    }).catch(function() {
      showToast("Network error -- could not update poll interval", "error");
    });
  }
  function setDefaultPolicy(value) {
    updateScanPreviews();
    fetch("/api/settings/default-policy", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ policy: value })
    }).then(function(resp) {
      return resp.json().then(function(data) {
        return { ok: resp.ok, data };
      });
    }).then(function(result) {
      if (result.ok) {
        showToast(result.data.message || "Default policy updated", "success");
      } else {
        showToast(result.data.error || "Failed to update default policy", "error");
      }
    }).catch(function() {
      showToast("Network error -- could not update default policy", "error");
    });
  }
  function setRollbackPolicy(value) {
    fetch("/api/settings/rollback-policy", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ policy: value })
    }).then(function(resp) {
      return resp.json().then(function(data) {
        return { ok: resp.ok, data };
      });
    }).then(function(result) {
      if (result.ok) {
        showToast(result.data.message || "Rollback policy updated", "success");
      } else {
        showToast(result.data.error || "Failed to update rollback policy", "error");
      }
    }).catch(function() {
      showToast("Network error -- could not update rollback policy", "error");
    });
  }
  function onGracePeriodChange(value) {
    var wrap = document.getElementById("grace-custom-wrap");
    if (value === "custom") {
      if (wrap) wrap.style.display = "";
      updateScanPreviews();
      return;
    }
    if (wrap) wrap.style.display = "none";
    setGracePeriod(value);
    updateScanPreviews();
  }
  function applyCustomGracePeriod() {
    var unit = document.getElementById("grace-custom-unit");
    var val = document.getElementById("grace-custom-value");
    if (!unit || !val || !unit.value || !val.value) return;
    setGracePeriod(val.value + unit.value);
    updateScanPreviews();
  }
  function setGracePeriod(duration) {
    fetch("/api/settings/grace-period", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ duration })
    }).then(function(resp) {
      return resp.json().then(function(data) {
        return { ok: resp.ok, data };
      });
    }).then(function(result) {
      if (result.ok) {
        showToast(result.data.message || "Grace period updated", "success");
      } else {
        showToast(result.data.error || "Failed to update grace period", "error");
      }
    }).catch(function() {
      showToast("Network error -- could not update grace period", "error");
    });
  }
  function setPauseState(paused) {
    updatePauseToggleText(paused);
    updateScanPreviews();
    fetch("/api/settings/pause", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ paused })
    }).then(function(resp) {
      return resp.json().then(function(data) {
        return { ok: resp.ok, data };
      });
    }).then(function(result) {
      if (result.ok) {
        showToast(result.data.message || (paused ? "Scanning paused" : "Scanning resumed"), "success");
      } else {
        showToast(result.data.error || "Failed to update pause state", "error");
      }
    }).catch(function() {
      showToast("Network error -- could not update pause state", "error");
    });
  }
  function updateLatestAutoText(enabled) {
    var text = document.getElementById("latest-auto-text");
    if (text) {
      text.textContent = enabled ? "On" : "Off";
    }
  }
  function setLatestAutoUpdate(enabled) {
    updateLatestAutoText(enabled);
    fetch("/api/settings/latest-auto-update", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ enabled })
    }).then(function(r) {
      return r.json();
    }).then(function(data) {
      showToast(data.message || "Setting updated", "success");
    }).catch(function() {
      showToast("Network error -- could not update setting", "error");
    });
  }
  function saveFilters() {
    var textarea = document.getElementById("container-filters");
    if (!textarea) return;
    var lines = textarea.value.split("\n");
    var patterns = [];
    for (var i = 0; i < lines.length; i++) {
      var trimmed = lines[i].replace(/^\s+|\s+$/g, "");
      if (trimmed !== "") {
        patterns.push(trimmed);
      }
    }
    fetch("/api/settings/filters", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ patterns })
    }).then(function(resp) {
      return resp.json().then(function(data) {
        return { ok: resp.ok, data };
      });
    }).then(function(result) {
      if (result.ok) {
        showToast(result.data.message || "Filters saved", "success");
      } else {
        showToast(result.data.error || "Failed to save filters", "error");
      }
    }).catch(function() {
      showToast("Network error -- could not save filters", "error");
    });
  }
  function updateToggleText(textId, enabled) {
    var text = document.getElementById(textId);
    if (text) {
      text.textContent = enabled ? "On" : "Off";
    }
  }
  function setImageCleanup(enabled) {
    updateToggleText("image-cleanup-text", enabled);
    fetch("/api/settings/image-cleanup", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ enabled }) }).then(function(r) {
      return r.json();
    }).then(function(data) {
      showToast(data.message || "Setting updated", "success");
    }).catch(function() {
      showToast("Network error -- could not update setting", "error");
    });
  }
  function saveCronSchedule() {
    var input = document.getElementById("cron-schedule");
    if (!input) return;
    updateScanPreviews();
    fetch("/api/settings/schedule", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ schedule: input.value }) }).then(function(resp) {
      return resp.json().then(function(data) {
        return { ok: resp.ok, data };
      });
    }).then(function(result) {
      if (result.ok) {
        showToast(result.data.message || "Schedule updated", "success");
      } else {
        showToast(result.data.error || "Failed to update schedule", "error");
      }
    }).catch(function() {
      showToast("Network error -- could not update schedule", "error");
    });
  }
  function setDependencyAware(enabled) {
    updateToggleText("dep-aware-text", enabled);
    fetch("/api/settings/dependency-aware", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ enabled }) }).then(function(r) {
      return r.json();
    }).then(function(data) {
      showToast(data.message || "Setting updated", "success");
    }).catch(function() {
      showToast("Network error -- could not update setting", "error");
    });
  }
  function setHooksEnabled(enabled) {
    updateToggleText("hooks-toggle-text", enabled);
    fetch("/api/settings/hooks-enabled", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ enabled }) }).then(function(r) {
      return r.json();
    }).then(function(data) {
      showToast(data.message || "Setting updated", "success");
    }).catch(function() {
      showToast("Network error -- could not update setting", "error");
    });
  }
  function setHooksWriteLabels(enabled) {
    updateToggleText("hooks-labels-text", enabled);
    fetch("/api/settings/hooks-write-labels", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ enabled }) }).then(function(r) {
      return r.json();
    }).then(function(data) {
      showToast(data.message || "Setting updated", "success");
    }).catch(function() {
      showToast("Network error -- could not update setting", "error");
    });
  }
  function setDryRun(enabled) {
    updateToggleText("dry-run-text", enabled);
    fetch("/api/settings/dry-run", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ enabled }) }).then(function(r) {
      return r.json();
    }).then(function(data) {
      showToast(data.message || "Setting updated", "success");
    }).catch(function() {
      showToast("Network error -- could not update setting", "error");
    });
  }
  function setPullOnly(enabled) {
    updateToggleText("pull-only-text", enabled);
    fetch("/api/settings/pull-only", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ enabled }) }).then(function(r) {
      return r.json();
    }).then(function(data) {
      showToast(data.message || "Setting updated", "success");
    }).catch(function() {
      showToast("Network error -- could not update setting", "error");
    });
  }
  function setComposeSync(enabled) {
    updateToggleText("compose-sync-text", enabled);
    fetch("/api/settings/compose-sync", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ enabled }) }).then(function(r) {
      return r.json();
    }).then(function(data) {
      showToast(data.message || "Setting updated", "success");
    }).catch(function() {
      showToast("Network error -- could not update setting", "error");
    });
  }
  function setImageBackup(enabled) {
    updateToggleText("image-backup-text", enabled);
    fetch("/api/settings/image-backup", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ enabled }) }).then(function(r) {
      return r.json();
    }).then(function(data) {
      showToast(data.message || "Setting updated", "success");
    }).catch(function() {
      showToast("Network error -- could not update setting", "error");
    });
  }
  function setShowStopped(enabled) {
    updateToggleText("show-stopped-text", enabled);
    fetch("/api/settings/show-stopped", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ enabled }) }).then(function(r) {
      return r.json();
    }).then(function(data) {
      showToast(data.message || "Setting updated", "success");
    }).catch(function() {
      showToast("Network error -- could not update setting", "error");
    });
  }
  function setRemoveVolumes(enabled) {
    updateToggleText("remove-volumes-text", enabled);
    fetch("/api/settings/remove-volumes", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ enabled }) }).then(function(r) {
      return r.json();
    }).then(function(data) {
      showToast(data.message || "Setting updated", "success");
    }).catch(function() {
      showToast("Network error -- could not update setting", "error");
    });
  }
  function setScanConcurrency() {
    var input = document.getElementById("scan-concurrency-input");
    var n = parseInt(input ? input.value : "1", 10);
    if (isNaN(n) || n < 1 || n > 20) {
      showToast("Concurrency must be between 1 and 20", "error");
      return;
    }
    fetch("/api/settings/scan-concurrency", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ concurrency: n }) }).then(function(r) {
      return r.json();
    }).then(function(data) {
      showToast(data.message || "Setting updated", "success");
    }).catch(function() {
      showToast("Network error -- could not update setting", "error");
    });
  }
  function setHADiscovery(enabled) {
    updateToggleText("ha-discovery-text", enabled);
    var prefix = (document.getElementById("ha-discovery-prefix") || {}).value || "";
    fetch("/api/settings/ha-discovery", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ enabled, prefix }) }).then(function(r) {
      if (r.status === 204) {
        showToast("HA discovery " + (enabled ? "enabled" : "disabled"), "success");
        return;
      }
      return r.json().then(function(d) {
        if (d.error) showToast(d.error, "error");
      });
    }).catch(function() {
      showToast("Network error -- could not update setting", "error");
    });
  }
  function saveHADiscoveryPrefix() {
    var prefix = (document.getElementById("ha-discovery-prefix") || {}).value || "";
    var enabled = (document.getElementById("ha-discovery-toggle") || {}).checked || false;
    fetch("/api/settings/ha-discovery", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ enabled, prefix }) }).then(function(r) {
      if (r.status === 204) {
        showToast("Discovery prefix saved", "success");
        return;
      }
      return r.json().then(function(d) {
        if (d.error) showToast(d.error, "error");
      });
    }).catch(function() {
      showToast("Network error -- could not save prefix", "error");
    });
  }
  function setUpdateDelay() {
    var input = document.getElementById("update-delay");
    if (!input) return;
    updateScanPreviews();
    fetch("/api/settings/update-delay", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ duration: input.value }) }).then(function(resp) {
      return resp.json().then(function(data) {
        return { ok: resp.ok, data };
      });
    }).then(function(result) {
      if (result.ok) {
        showToast(result.data.message || "Update delay saved", "success");
      } else {
        showToast(result.data.error || "Failed to save update delay", "error");
      }
    }).catch(function() {
      showToast("Network error -- could not save update delay", "error");
    });
  }
  function toggleCollapsible(headerEl) {
    var expanded = headerEl.getAttribute("aria-expanded") === "true";
    headerEl.setAttribute("aria-expanded", expanded ? "false" : "true");
    var body = headerEl.nextElementSibling;
    if (body) {
      body.style.display = expanded ? "none" : "";
    }
  }
  function saveDockerTLS() {
    var ca = (document.getElementById("docker-tls-ca") || {}).value || "";
    var cert = (document.getElementById("docker-tls-cert") || {}).value || "";
    var key = (document.getElementById("docker-tls-key") || {}).value || "";
    fetch("/api/settings/docker-tls", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ca, cert, key })
    }).then(function(resp) {
      return resp.json().then(function(data) {
        return { ok: resp.ok, data };
      });
    }).then(function(result) {
      if (result.ok) {
        showToast(result.data.message || "Docker TLS settings saved", "success");
        var banner = document.getElementById("general-restart-banner");
        if (banner) banner.style.display = "block";
      } else {
        showToast(result.data.error || "Failed to save Docker TLS settings", "error");
      }
    }).catch(function() {
      showToast("Network error -- could not save Docker TLS settings", "error");
    });
  }
  function testDockerTLS() {
    var ca = (document.getElementById("docker-tls-ca") || {}).value || "";
    var cert = (document.getElementById("docker-tls-cert") || {}).value || "";
    var key = (document.getElementById("docker-tls-key") || {}).value || "";
    if (!ca || !cert || !key) {
      showToast("All three certificate paths are required for testing", "error");
      return;
    }
    var btn = document.getElementById("docker-tls-test-btn");
    if (btn) btn.disabled = true;
    fetch("/api/settings/docker-tls-test", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ca, cert, key })
    }).then(function(resp) {
      return resp.json();
    }).then(function(data) {
      if (data.success) {
        showToast("Docker TLS connection successful", "success");
      } else {
        showToast("Connection failed: " + (data.error || "unknown error"), "error");
      }
    }).catch(function() {
      showToast("Network error -- could not test Docker TLS connection", "error");
    }).finally(function() {
      if (btn) btn.disabled = false;
    });
  }
  function loadWebhookSettings() {
    var toggle = document.getElementById("webhook-enabled-toggle");
    if (!toggle) return;
    fetch("/api/settings/webhook-info").then(function(r) {
      return r.json();
    }).then(function(data) {
      var enabled = data.enabled === "true";
      toggle.checked = enabled;
      updateToggleText("webhook-enabled-text", enabled);
      showWebhookConfig(enabled, data.secret || "");
    }).catch(function() {
    });
  }
  function showWebhookConfig(enabled, secret) {
    var configDiv = document.getElementById("webhook-config");
    if (!configDiv) return;
    configDiv.style.display = enabled ? "" : "none";
    var urlInput = document.getElementById("webhook-url");
    if (urlInput) {
      urlInput.value = window.location.origin + "/api/webhook";
    }
    var secretInput = document.getElementById("webhook-secret");
    if (secretInput) {
      secretInput.value = secret;
      var hint = document.getElementById("webhook-secret-hint");
      if (hint) {
        hint.style.display = secret && secret.indexOf("****") !== -1 ? "" : "none";
      }
    }
  }
  function setWebhookEnabled(enabled) {
    updateToggleText("webhook-enabled-text", enabled);
    fetch("/api/settings/webhook-enabled", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ enabled })
    }).then(function(resp) {
      return resp.json().then(function(data) {
        return { ok: resp.ok, data };
      });
    }).then(function(result) {
      if (result.ok) {
        showToast(result.data.message || "Setting updated", "success");
        loadWebhookSettings();
      } else {
        showToast(result.data.error || "Failed to update setting", "error");
      }
    }).catch(function() {
      showToast("Network error -- could not update setting", "error");
    });
  }
  function regenerateWebhookSecret() {
    if (!confirm("This will invalidate all existing webhook integrations. Continue?")) return;
    fetch("/api/settings/webhook-secret", {
      method: "POST",
      headers: { "Content-Type": "application/json" }
    }).then(function(resp) {
      return resp.json().then(function(data) {
        return { ok: resp.ok, data };
      });
    }).then(function(result) {
      if (result.ok) {
        var secretInput = document.getElementById("webhook-secret");
        if (secretInput) secretInput.value = result.data.secret || "";
        var hint = document.getElementById("webhook-secret-hint");
        if (hint) hint.style.display = "none";
        showToast("Webhook secret regenerated \u2014 copy it now, it won't be shown again", "success");
      } else {
        showToast(result.data.error || "Failed to regenerate secret", "error");
      }
    }).catch(function() {
      showToast("Network error -- could not regenerate secret", "error");
    });
  }
  function copyWebhookURL() {
    var input = document.getElementById("webhook-url");
    if (!input || !input.value) return;
    navigator.clipboard.writeText(input.value).then(function() {
      showToast("Webhook URL copied", "success");
    }).catch(function() {
      input.select();
      document.execCommand("copy");
      showToast("Webhook URL copied", "success");
    });
  }
  function copyWebhookSecret() {
    var input = document.getElementById("webhook-secret");
    if (!input || !input.value) return;
    navigator.clipboard.writeText(input.value).then(function() {
      showToast("Webhook secret copied", "success");
    }).catch(function() {
      input.select();
      document.execCommand("copy");
      showToast("Webhook secret copied", "success");
    });
  }
  function saveMaintenanceWindow() {
    var input = document.getElementById("maintenance-window");
    if (!input) return;
    updateScanPreviews();
    fetch("/api/settings/maintenance-window", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ value: input.value.trim() })
    }).then(function(resp) {
      return resp.json().then(function(data) {
        return { ok: resp.ok, data };
      });
    }).then(function(result) {
      if (result.ok) {
        showToast(result.data.message || "Maintenance window saved", "success");
      } else {
        showToast(result.data.error || "Failed to save maintenance window", "error");
      }
    }).catch(function() {
      showToast("Network error -- could not save maintenance window", "error");
    });
  }
  function exportConfig() {
    var includeSecrets = document.getElementById("export-secrets-toggle");
    var qs = includeSecrets && includeSecrets.checked ? "?secrets=true" : "";
    fetch("/api/config/export" + qs).then(function(r) {
      if (!r.ok) throw new Error("Export failed");
      return r.blob();
    }).then(function(blob) {
      var a = document.createElement("a");
      a.href = URL.createObjectURL(blob);
      a.download = "sentinel-config-" + (/* @__PURE__ */ new Date()).toISOString().slice(0, 10) + ".json";
      a.click();
      URL.revokeObjectURL(a.href);
      showToast("Configuration exported", "success");
    }).catch(function() {
      showToast("Export failed", "error");
    });
  }
  function importConfig() {
    var fileInput = document.getElementById("config-import-file");
    if (!fileInput || !fileInput.files.length) {
      showToast("Select a file first", "error");
      return;
    }
    if (!confirm("Import will overwrite matching settings. Continue?")) return;
    var file = fileInput.files[0];
    var reader = new FileReader();
    reader.onload = function(e) {
      fetch("/api/config/import", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: e.target.result
      }).then(function(r) {
        return r.json();
      }).then(function(data) {
        if (data.error) {
          showToast(data.error, "error");
        } else {
          showToast(data.message || "Configuration imported", "success");
          setTimeout(function() {
            location.reload();
          }, 1e3);
        }
      }).catch(function() {
        showToast("Import failed", "error");
      });
    };
    reader.readAsText(file);
  }

  // internal/web/static/src/js/settings-cluster.js
  function _updateToggleText(textId, enabled) {
    if (window.updateToggleText) {
      window.updateToggleText(textId, enabled);
    } else {
      var text = document.getElementById(textId);
      if (text) text.textContent = enabled ? "On" : "Off";
    }
  }
  function loadClusterSettings() {
    if (!document.getElementById("cluster-enabled")) return;
    fetch("/api/settings/cluster").then(function(r) {
      return r.json();
    }).then(function(s) {
      var enabled = s.enabled === "true";
      document.getElementById("cluster-enabled").checked = enabled;
      _updateToggleText("cluster-enabled-text", enabled);
      document.getElementById("cluster-port").value = s.port || "9443";
      document.getElementById("cluster-grace").value = s.grace_period || "30m";
      document.getElementById("cluster-policy").value = s.remote_policy || "manual";
      var autoUpdate = s.auto_update_agents === "true";
      document.getElementById("cluster-auto-update").checked = autoUpdate;
      _updateToggleText("cluster-auto-update-text", autoUpdate);
      toggleClusterFields(enabled);
    }).catch(function(err) {
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
    var autoUpdateEl = document.getElementById("cluster-auto-update");
    _updateToggleText("cluster-auto-update-text", autoUpdateEl.checked);
    fetch("/api/settings/cluster", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        enabled,
        port: document.getElementById("cluster-port").value,
        grace_period: document.getElementById("cluster-grace").value,
        remote_policy: document.getElementById("cluster-policy").value,
        auto_update_agents: autoUpdateEl.checked
      })
    }).then(function(resp) {
      return resp.json().then(function(data) {
        return { ok: resp.ok, data };
      });
    }).then(function(result) {
      if (result.ok) {
        showToast("Cluster settings saved", "success");
      } else {
        showToast(result.data.error || "Failed to save cluster settings", "error");
      }
    }).catch(function() {
      showToast("Network error -- could not save cluster settings", "error");
    });
  }

  // internal/web/static/src/js/notifications.js
  var EVENT_TYPES = [
    { key: "update_available", label: "Update Available" },
    { key: "update_started", label: "Update Started" },
    { key: "update_succeeded", label: "Update Succeeded" },
    { key: "update_failed", label: "Update Failed" },
    { key: "rollback_succeeded", label: "Rollback Succeeded" },
    { key: "rollback_failed", label: "Rollback Failed" },
    { key: "container_state", label: "State Change" }
  ];
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
    ]
  };
  var notificationChannels = [];
  function loadNotificationChannels() {
    fetch("/api/settings/notifications").then(function(r) {
      return r.json();
    }).then(function(data) {
      if (Array.isArray(data)) {
        notificationChannels = data;
      } else {
        notificationChannels = [];
      }
      renderChannels();
    }).catch(function() {
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
    try {
      settings = JSON.parse(ch.settings || "{}");
    } catch (e2) {
      settings = ch.settings || {};
    }
    var card = document.createElement("div");
    card.className = "channel-card";
    card.setAttribute("data-index", index);
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
    testBtn.onclick = function() {
      testChannel(parseInt(this.getAttribute("data-index")));
    };
    actions.appendChild(testBtn);
    var delBtn = document.createElement("button");
    delBtn.className = "btn btn-error";
    delBtn.textContent = "Delete";
    delBtn.setAttribute("data-index", index);
    delBtn.onclick = function() {
      deleteChannel(parseInt(this.getAttribute("data-index")));
    };
    actions.appendChild(delBtn);
    header.appendChild(actions);
    card.appendChild(header);
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
      var val = settings[field.key];
      if (field.key === "headers" && val && typeof val === "object") {
        input.value = JSON.stringify(val);
      } else if (field.key === "priority" && val !== void 0) {
        input.value = String(val);
      } else {
        input.value = val || "";
      }
      row.appendChild(input);
      fieldsDiv.appendChild(row);
    }
    card.appendChild(fieldsDiv);
    var enabledEvents = ch.events;
    var allEnabled = !enabledEvents || enabledEvents.length === 0;
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
      type,
      name,
      enabled: true,
      settings: "{}",
      events: defaultEvents
    });
    select.value = "";
    renderChannels();
    showToast("Added " + name + " channel \u2014 configure and save", "info");
  }
  function deleteChannel(index) {
    if (index < 0 || index >= notificationChannels.length) return;
    var name = notificationChannels[index].name || notificationChannels[index].type;
    notificationChannels.splice(index, 1);
    renderChannels();
    showToast("Removed " + name + " \u2014 save to apply", "info");
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
      try {
        settings = JSON.parse(notificationChannels[idx].settings || "{}");
      } catch (e) {
        settings = {};
      }
      if (typeof notificationChannels[idx].settings === "object" && notificationChannels[idx].settings !== null && !(notificationChannels[idx].settings instanceof String)) {
        settings = notificationChannels[idx].settings;
      }
      var inputs = cards[i].querySelectorAll("[data-setting]");
      for (var j = 0; j < inputs.length; j++) {
        var key = inputs[j].getAttribute("data-setting");
        var val = inputs[j].value;
        if (key === "headers") {
          try {
            settings[key] = JSON.parse(val);
          } catch (e) {
            settings[key] = {};
          }
        } else if (key === "priority") {
          settings[key] = parseInt(val) || 3;
        } else {
          settings[key] = val;
        }
      }
      notificationChannels[idx].settings = JSON.stringify(settings);
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
    if (btn) {
      btn.classList.add("loading");
      btn.disabled = true;
    }
    var payload = [];
    for (var i = 0; i < notificationChannels.length; i++) {
      var ch = {};
      var keys = Object.keys(notificationChannels[i]);
      for (var k = 0; k < keys.length; k++) {
        ch[keys[k]] = notificationChannels[i][keys[k]];
      }
      if (typeof ch.settings === "string") {
        try {
          ch.settings = JSON.parse(ch.settings);
        } catch (e) {
          ch.settings = {};
        }
      }
      payload.push(ch);
    }
    fetch("/api/settings/notifications", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload)
    }).then(function(resp) {
      return resp.json().then(function(data) {
        return { ok: resp.ok, data };
      });
    }).then(function(result) {
      if (result.ok) {
        showToast(result.data.message || "Notification settings saved", "success");
        loadNotificationChannels();
      } else {
        showToast(result.data.error || "Failed to save notification settings", "error");
      }
    }).catch(function() {
      showToast("Network error \u2014 could not save notification settings", "error");
    }).finally(function() {
      if (btn) {
        btn.classList.remove("loading");
        btn.disabled = false;
      }
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
    fetch("/api/settings/digest", { credentials: "same-origin" }).then(function(res) {
      return res.json();
    }).then(function(data) {
      var mode = data.default_notify_mode || "default";
      var radios = document.querySelectorAll('input[name="default-notify-mode"]');
      for (var i = 0; i < radios.length; i++) {
        radios[i].checked = radios[i].value === mode;
      }
      updateDigestScheduleVisibility(mode);
      updateNotifyModePreview(mode);
      var el = document.getElementById("digest-time");
      if (el) el.value = data.digest_time || "09:00";
      el = document.getElementById("digest-interval");
      if (el) el.value = data.digest_interval || "24h";
    }).catch(function() {
    });
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
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body)
    }).then(function(res) {
      return res.json();
    }).then(function(data) {
      if (data.status === "ok") showToast("Settings saved", "success");
    }).catch(function() {
      showToast("Failed to save settings", "error");
    });
  }
  function triggerDigest() {
    fetch("/api/digest/trigger", {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" }
    }).then(function(res) {
      return res.json();
    }).then(function(data) {
      showToast(data.message || "Digest triggered", "info");
    }).catch(function() {
      showToast("Failed to trigger digest", "error");
    });
  }
  function loadContainerNotifyPrefs() {
    var container = document.getElementById("container-notify-prefs");
    if (!container) return;
    fetch("/api/settings/container-notify-prefs", { credentials: "same-origin" }).then(function(res) {
      return res.json();
    }).then(function(prefs) {
      fetch("/api/containers", { credentials: "same-origin" }).then(function(res) {
        return res.json();
      }).then(function(containers) {
        renderContainerNotifyPrefs(container, containers, prefs);
      });
    }).catch(function() {
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
      { value: "default", label: "Immediate + summary" },
      { value: "every_scan", label: "Every scan" },
      { value: "digest_only", label: "Summary only" },
      { value: "muted", label: "Silent" }
    ];
    var items = [];
    var overrideCount = 0;
    for (var i = 0; i < containers.length; i++) {
      var mode = prefs[containers[i].name] && prefs[containers[i].name].mode || "default";
      if (mode !== "default") overrideCount++;
      items.push({ name: containers[i].name, mode, stack: containers[i].stack || "" });
    }
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
    for (var sk in stackMap) {
      stackMap[sk].sort(function(a, b) {
        return a.name.localeCompare(b.name);
      });
    }
    var summary = document.createElement("p");
    summary.className = "notify-prefs-summary";
    summary.textContent = overrideCount === 0 ? "All " + items.length + " containers use the default notification mode. Select containers below to set a different mode." : overrideCount + " of " + items.length + " containers have custom settings. Select containers to change their mode.";
    el.appendChild(summary);
    var toolbar = document.createElement("div");
    toolbar.className = "notify-prefs-toolbar";
    var selectAllBtn = document.createElement("button");
    selectAllBtn.className = "btn";
    selectAllBtn.textContent = "Select all";
    selectAllBtn.addEventListener("click", function() {
      toggleAllPrefs(true);
    });
    toolbar.appendChild(selectAllBtn);
    var deselectBtn = document.createElement("button");
    deselectBtn.className = "btn";
    deselectBtn.textContent = "Deselect all";
    deselectBtn.addEventListener("click", function() {
      toggleAllPrefs(false);
    });
    toolbar.appendChild(deselectBtn);
    el.appendChild(toolbar);
    var listWrap = document.createElement("div");
    listWrap.id = "notify-prefs-list";
    for (var g = 0; g < stackOrder.length; g++) {
      var stackName = stackOrder[g];
      var groupItems = stackMap[stackName];
      var groupCard = document.createElement("div");
      groupCard.className = "notify-prefs-group";
      var heading = document.createElement("div");
      heading.className = "notify-prefs-group-heading";
      heading.textContent = stackName === "swarm" ? "Swarm Services" : stackName || "Standalone";
      groupCard.appendChild(heading);
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
    applyBtn.addEventListener("click", function() {
      applyPrefsToSelected();
    });
    actionBar.appendChild(applyBtn);
    var resetBtn = document.createElement("button");
    resetBtn.className = "btn";
    resetBtn.textContent = "Reset to default";
    resetBtn.addEventListener("click", function() {
      applyPrefsToSelected("default");
    });
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
    if (!confirm(action + " " + cbs.length + " container" + (cbs.length > 1 ? "s" : "") + ' to "' + label + '"?')) return;
    var pending = cbs.length;
    for (var i = 0; i < cbs.length; i++) {
      (function(name) {
        fetch("/api/containers/" + encodeURIComponent(name) + "/notify-pref", {
          method: "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ mode })
        }).then(function() {
          pending--;
          if (pending === 0) {
            showToast(action + " " + cbs.length + " containers to " + label, "success");
            loadContainerNotifyPrefs();
          }
        }).catch(function() {
          pending--;
        });
      })(cbs[i].value);
    }
  }
  function setContainerNotifyPref(name, mode) {
    fetch("/api/containers/" + encodeURIComponent(name) + "/notify-pref", {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ mode })
    }).then(function(res) {
      return res.json();
    }).then(function(data) {
      if (data.status === "ok") {
        showToast("Notification mode updated for " + name, "success");
        loadContainerNotifyPrefs();
      }
    }).catch(function() {
      showToast("Failed to update notification mode", "error");
    });
  }
  var _notifyTemplates = {};
  function loadNotifyTemplates() {
    fetch("/api/settings/notifications/templates", { credentials: "same-origin" }).then(function(r) {
      return r.json();
    }).then(function(data) {
      _notifyTemplates = data && data.templates ? data.templates : {};
      loadTemplateForEvent();
    }).catch(function() {
      _notifyTemplates = {};
    });
  }
  function loadTemplateForEvent() {
    var sel = document.getElementById("template-event-type");
    var textarea = document.getElementById("template-body");
    if (!sel || !textarea) return;
    var eventType = sel.value;
    textarea.value = _notifyTemplates[eventType] || "";
    var previewOut = document.getElementById("template-preview-output");
    if (previewOut) previewOut.style.display = "none";
  }
  function saveNotifyTemplate() {
    var sel = document.getElementById("template-event-type");
    var textarea = document.getElementById("template-body");
    if (!sel || !textarea) return;
    var eventType = sel.value;
    var tmpl = textarea.value;
    fetch("/api/settings/notifications/templates", {
      method: "PUT",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ event_type: eventType, template: tmpl })
    }).then(function(resp) {
      return resp.json().then(function(data) {
        return { ok: resp.ok, data };
      });
    }).then(function(result) {
      if (result.ok) {
        _notifyTemplates[eventType] = tmpl;
        showToast("Template saved for " + eventType, "success");
      } else {
        showToast(result.data.error || "Failed to save template", "error");
      }
    }).catch(function() {
      showToast("Network error \u2014 could not save template", "error");
    });
  }
  function deleteNotifyTemplate() {
    var sel = document.getElementById("template-event-type");
    var textarea = document.getElementById("template-body");
    if (!sel) return;
    var eventType = sel.value;
    fetch("/api/settings/notifications/templates/" + encodeURIComponent(eventType), {
      method: "DELETE",
      credentials: "same-origin"
    }).then(function(resp) {
      return resp.json().then(function(data) {
        return { ok: resp.ok, data };
      });
    }).then(function(result) {
      if (result.ok) {
        delete _notifyTemplates[eventType];
        if (textarea) textarea.value = "";
        var previewOut = document.getElementById("template-preview-output");
        if (previewOut) previewOut.style.display = "none";
        showToast("Template reset to default for " + eventType, "success");
      } else {
        showToast(result.data.error || "Failed to reset template", "error");
      }
    }).catch(function() {
      showToast("Network error \u2014 could not reset template", "error");
    });
  }
  function previewNotifyTemplate() {
    var sel = document.getElementById("template-event-type");
    var textarea = document.getElementById("template-body");
    if (!sel || !textarea) return;
    var eventType = sel.value;
    var tmpl = textarea.value;
    if (!tmpl) {
      showToast("Enter a template to preview", "info");
      return;
    }
    fetch("/api/settings/notifications/templates/preview", {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ event_type: eventType, template: tmpl })
    }).then(function(resp) {
      return resp.json().then(function(data) {
        return { ok: resp.ok, data };
      });
    }).then(function(result) {
      var previewOut = document.getElementById("template-preview-output");
      var previewText = document.getElementById("template-preview-text");
      if (!previewOut || !previewText) return;
      if (result.ok) {
        previewText.textContent = result.data.preview || "";
        previewOut.style.display = "";
      } else {
        previewText.textContent = "Error: " + (result.data.error || "invalid template");
        previewOut.style.display = "";
      }
    }).catch(function() {
      showToast("Network error \u2014 could not preview template", "error");
    });
  }

  // internal/web/static/src/js/registries.js
  var registryData = {};
  var registryCredentials = [];
  function loadRegistries() {
    fetch("/api/settings/registries").then(function(r) {
      return r.json();
    }).then(function(data) {
      registryData = data;
      registryCredentials = [];
      Object.keys(data).forEach(function(reg) {
        if (data[reg].credential) {
          registryCredentials.push(data[reg].credential);
        }
      });
      renderRegistryStatus();
      renderRegistryCredentials();
    }).catch(function(err) {
      console.error("Failed to load registries:", err);
    });
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
          var now = /* @__PURE__ */ new Date();
          var diffMs = resetTime - now;
          if (diffMs > 0) {
            var hours = Math.floor(diffMs / 36e5);
            var mins = Math.floor(diffMs % 36e5 / 6e4);
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
        alertDiv.textContent = "\u26A0 " + reg + ": No credentials. Unauthenticated rate limits apply.";
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
    }
    registryCredentials.forEach(function(cred, index) {
      var card = document.createElement("div");
      card.className = "channel-card";
      card.setAttribute("data-index", index);
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
      testBtn.addEventListener("click", function() {
        testRegistryCredential(parseInt(this.getAttribute("data-index"), 10));
      });
      actions.appendChild(testBtn);
      var delBtn = document.createElement("button");
      delBtn.className = "btn btn-sm btn-error";
      delBtn.textContent = "Remove";
      delBtn.setAttribute("data-index", index);
      delBtn.addEventListener("click", function() {
        deleteRegistryCredential(parseInt(this.getAttribute("data-index"), 10));
      });
      actions.appendChild(delBtn);
      header.appendChild(actions);
      card.appendChild(header);
      var regHidden = document.createElement("input");
      regHidden.type = "hidden";
      regHidden.setAttribute("data-field", "registry");
      regHidden.value = cred.registry;
      card.appendChild(regHidden);
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
    var addSelect = document.getElementById("registry-type-select");
    if (addSelect) {
      while (addSelect.options.length > 1) addSelect.remove(1);
      var usedRegs = {};
      registryCredentials.forEach(function(c) {
        usedRegs[c.registry] = true;
      });
      var allRegs = Object.keys(registryData).sort();
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
    registryCredentials.push({ id, registry: reg, username: "", secret: "" });
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
    }).then(function(r) {
      return r.json();
    }).then(function(data) {
      if (btn) btn.classList.remove("loading");
      if (data.error) {
        showToast("Failed: " + data.error, "error");
      } else {
        showToast("Registry credentials saved", "success");
        loadRegistries();
        setTimeout(function() {
          loadRegistries();
        }, 3e3);
      }
    }).catch(function(err) {
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
    }).then(function(r) {
      return r.json();
    }).then(function(data) {
      if (data.success) {
        showToast("Credentials valid for " + cred.registry, "success");
        setTimeout(function() {
          fetch("/api/settings/registries").then(function(r) {
            return r.json();
          }).then(function(d) {
            registryData = d;
            renderRegistryStatus();
          });
        }, 3e3);
      } else {
        showToast("Test failed: " + (data.error || "unknown error"), "error");
      }
    }).catch(function(err) {
      showToast("Test failed: " + err, "error");
    });
  }
  function updateRateLimitStatus() {
    fetch("/api/ratelimits").then(function(r) {
      return r.json();
    }).then(function(data) {
      var el = document.getElementById("rate-limit-status");
      if (!el) return;
      var health = data.health || "ok";
      var labels = { ok: "Healthy", low: "Needs Attention", exhausted: "Exhausted" };
      el.textContent = labels[health] || "Healthy";
      el.className = "stat-value";
      if (health === "ok") el.classList.add("success");
      else if (health === "low") el.classList.add("warning");
      else if (health === "exhausted") el.classList.add("error");
    }).catch(function() {
    });
  }
  if (document.getElementById("rate-limit-status")) {
    updateRateLimitStatus();
  }

  // internal/web/static/src/js/about.js
  function loadFooterVersion() {
    var el = document.getElementById("footer-version");
    if (!el) return;
    fetch("/api/about").then(function(r) {
      return r.json();
    }).then(function(data) {
      el.textContent = "Docker-Sentinel " + (data.version || "dev");
    }).catch(function() {
    });
  }
  function loadAboutInfo() {
    var container = document.getElementById("about-content");
    if (!container) return;
    fetch("/api/about").then(function(r) {
      return r.json();
    }).then(function(data) {
      var rows = document.createElement("div");
      rows.className = "settings-rows";
      appendAboutSection(rows, "Instance");
      appendAboutRow(rows, "Version", data.version || "dev");
      appendAboutRow(rows, "Go Version", data.go_version || "-");
      appendAboutRow(rows, "Data Directory", data.data_directory || "-");
      appendAboutRow(rows, "Uptime", data.uptime || "-");
      appendAboutRow(rows, "Started", data.started_at ? formatAboutTime(data.started_at) : "-");
      appendAboutSection(rows, "Runtime");
      appendAboutRow(rows, "Poll Interval", data.poll_interval || "-");
      appendAboutRow(rows, "Last Scan", data.last_scan ? formatAboutTimeAgo(data.last_scan) : "Never");
      appendAboutRow(rows, "Containers Monitored", String(data.containers || 0));
      appendAboutRow(rows, "Updates Applied", String(data.updates_applied || 0));
      appendAboutRow(rows, "Snapshots Stored", String(data.snapshots || 0));
      appendAboutSection(rows, "Integrations");
      if (data.channels && data.channels.length > 0) {
        var chWrap = document.createElement("div");
        chWrap.className = "about-channels";
        for (var i = 0; i < data.channels.length; i++) {
          var badge = document.createElement("span");
          badge.className = "about-channel-badge";
          badge.textContent = data.channels[i].name;
          var typeSpan = document.createElement("span");
          typeSpan.className = "about-channel-type";
          typeSpan.textContent = data.channels[i].type;
          badge.appendChild(typeSpan);
          chWrap.appendChild(badge);
        }
        appendAboutRowEl(rows, "Notification Channels", chWrap);
      } else {
        appendAboutRow(rows, "Notification Channels", "None configured");
      }
      if (data.registries && data.registries.length > 0) {
        var regWrap = document.createElement("div");
        regWrap.className = "about-channels";
        for (var i = 0; i < data.registries.length; i++) {
          var regBadge = document.createElement("span");
          regBadge.className = "about-channel-badge";
          regBadge.textContent = data.registries[i];
          regWrap.appendChild(regBadge);
        }
        appendAboutRowEl(rows, "Registry Auth", regWrap);
      } else {
        appendAboutRow(rows, "Registry Auth", "None configured");
      }
      var banner = document.createElement("div");
      banner.className = "about-banner";
      var bannerIcon = document.createElement("span");
      bannerIcon.className = "about-banner-icon";
      bannerIcon.textContent = "\u24D8";
      banner.appendChild(bannerIcon);
      var bannerText = document.createElement("span");
      bannerText.textContent = "This is BETA software. Features may be broken and/or unstable. Please report any issues on ";
      var bannerLink = document.createElement("a");
      bannerLink.href = "https://github.com/Will-Luck/Docker-Sentinel/issues";
      bannerLink.target = "_blank";
      bannerLink.rel = "noopener";
      bannerLink.textContent = "GitHub";
      bannerText.appendChild(bannerLink);
      bannerText.appendChild(document.createTextNode("!"));
      banner.appendChild(bannerText);
      appendAboutSection(rows, "Links");
      var linksWrap = document.createElement("div");
      linksWrap.className = "about-links";
      var links = [
        { icon: "\u{1F4C1}", label: "GitHub", url: "https://github.com/Will-Luck/Docker-Sentinel" },
        { icon: "\u{1F41B}", label: "Report a Bug", url: "https://github.com/Will-Luck/Docker-Sentinel/issues/new?template=bug_report.md" },
        { icon: "\u{1F4A1}", label: "Feature Request", url: "https://github.com/Will-Luck/Docker-Sentinel/issues/new?template=feature_request.md" },
        { icon: "\u{1F4C4}", label: "Releases", url: "https://github.com/Will-Luck/Docker-Sentinel/releases" }
      ];
      for (var li = 0; li < links.length; li++) {
        var a = document.createElement("a");
        a.className = "about-link";
        a.href = links[li].url;
        a.target = "_blank";
        a.rel = "noopener";
        var ico = document.createElement("span");
        ico.className = "about-link-icon";
        ico.textContent = links[li].icon;
        a.appendChild(ico);
        a.appendChild(document.createTextNode(links[li].label));
        linksWrap.appendChild(a);
      }
      var linksRow = document.createElement("div");
      linksRow.className = "setting-row";
      linksRow.appendChild(linksWrap);
      rows.appendChild(linksRow);
      container.textContent = "";
      container.appendChild(banner);
      container.appendChild(rows);
    }).catch(function() {
      container.textContent = "Failed to load info";
    });
  }
  function appendAboutSection(parent, title) {
    var div = document.createElement("div");
    div.className = "about-section-title";
    div.textContent = title;
    parent.appendChild(div);
  }
  function appendAboutRow(parent, label, value) {
    var row = document.createElement("div");
    row.className = "setting-row";
    var info = document.createElement("div");
    info.className = "setting-info";
    var lbl = document.createElement("div");
    lbl.className = "setting-label";
    lbl.textContent = label;
    info.appendChild(lbl);
    row.appendChild(info);
    var val = document.createElement("div");
    val.className = "about-value";
    val.textContent = value;
    row.appendChild(val);
    parent.appendChild(row);
  }
  function appendAboutRowEl(parent, label, valueEl) {
    var row = document.createElement("div");
    row.className = "setting-row";
    var info = document.createElement("div");
    info.className = "setting-info";
    var lbl = document.createElement("div");
    lbl.className = "setting-label";
    lbl.textContent = label;
    info.appendChild(lbl);
    row.appendChild(info);
    row.appendChild(valueEl);
    parent.appendChild(row);
  }
  function formatAboutTime(iso) {
    try {
      var d = new Date(iso);
      return d.toLocaleDateString(void 0, { year: "numeric", month: "short", day: "numeric" }) + " " + d.toLocaleTimeString(void 0, { hour: "2-digit", minute: "2-digit" });
    } catch (e) {
      return iso;
    }
  }
  function formatAboutTimeAgo(iso) {
    try {
      var d = new Date(iso);
      var now = /* @__PURE__ */ new Date();
      var diff = now - d;
      var mins = Math.floor(diff / 6e4);
      if (mins < 1) return "Just now";
      if (mins < 60) return mins + "m ago";
      var hours = Math.floor(mins / 60);
      if (hours < 24) return hours + "h " + mins % 60 + "m ago";
      var days = Math.floor(hours / 24);
      return days + "d " + hours % 24 + "h ago";
    } catch (e) {
      return iso;
    }
  }
  var releaseSources = [];
  function loadReleaseSources() {
    var container = document.getElementById("release-sources-list");
    if (!container) return;
    fetch("/api/release-sources").then(function(r) {
      return r.json();
    }).then(function(data) {
      releaseSources = Array.isArray(data) ? data : [];
      renderReleaseSources();
    }).catch(function() {
    });
  }
  function renderReleaseSources() {
    var container = document.getElementById("release-sources-list");
    if (!container) return;
    container.textContent = "";
    if (releaseSources.length === 0) {
      var empty = document.createElement("div");
      empty.className = "empty-state";
      empty.style.padding = "var(--sp-6) var(--sp-4)";
      var h3 = document.createElement("h3");
      h3.textContent = "No custom sources configured";
      var p = document.createElement("p");
      p.textContent = "Add mappings to fetch release notes for images not covered by built-in rules.";
      empty.appendChild(h3);
      empty.appendChild(p);
      container.appendChild(empty);
      return;
    }
    releaseSources.forEach(function(src, index) {
      var card = document.createElement("div");
      card.className = "channel-card";
      var header = document.createElement("div");
      header.className = "channel-card-header";
      var badge = document.createElement("span");
      badge.className = "channel-type-badge";
      badge.textContent = src.image_pattern || "source";
      header.appendChild(badge);
      var actions = document.createElement("div");
      actions.className = "channel-actions";
      var delBtn = document.createElement("button");
      delBtn.className = "btn btn-sm btn-error";
      delBtn.textContent = "Remove";
      (function(i) {
        delBtn.addEventListener("click", function() {
          deleteReleaseSource(i);
        });
      })(index);
      actions.appendChild(delBtn);
      header.appendChild(actions);
      card.appendChild(header);
      var fields = document.createElement("div");
      fields.className = "channel-fields";
      [
        { label: "Image Pattern", field: "image_pattern", value: src.image_pattern },
        { label: "GitHub Repo", field: "github_repo", value: src.github_repo }
      ].forEach(function(def) {
        var row = document.createElement("div");
        row.className = "channel-field";
        var lbl = document.createElement("span");
        lbl.className = "channel-field-label";
        lbl.textContent = def.label;
        row.appendChild(lbl);
        var inp = document.createElement("input");
        inp.type = "text";
        inp.className = "channel-field-input";
        inp.value = def.value || "";
        inp.setAttribute("data-index", index);
        inp.setAttribute("data-field", def.field);
        row.appendChild(inp);
        fields.appendChild(row);
      });
      card.appendChild(fields);
      container.appendChild(card);
    });
  }
  function addReleaseSource() {
    releaseSources.push({ image_pattern: "", github_repo: "" });
    renderReleaseSources();
  }
  function deleteReleaseSource(index) {
    releaseSources.splice(index, 1);
    renderReleaseSources();
  }
  function collectReleaseSourcesFromDOM() {
    var inputs = document.querySelectorAll("#release-sources-list input[data-field]");
    var map = {};
    inputs.forEach(function(inp) {
      var i = inp.getAttribute("data-index");
      var f = inp.getAttribute("data-field");
      if (!map[i]) map[i] = {};
      map[i][f] = inp.value.trim();
    });
    return Object.keys(map).sort(function(a, b) {
      return a - b;
    }).map(function(k) {
      return map[k];
    });
  }
  function saveReleaseSources(event) {
    if (event) event.preventDefault();
    var sources = collectReleaseSourcesFromDOM();
    fetch("/api/release-sources", {
      method: "PUT",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": getCSRFToken() },
      body: JSON.stringify(sources)
    }).then(function(r) {
      if (r.ok) {
        releaseSources = sources;
        showToast("Release sources saved", "success");
      } else {
        r.json().then(function(d) {
          showToast("Save failed: " + (d.error || r.status), "error");
        });
      }
    }).catch(function() {
      showToast("Save failed", "error");
    });
  }

  // internal/web/static/src/js/images.js
  var _allImages = [];
  var _currentFilter = "all";
  var _currentSort = "default";
  var _manageMode = false;
  var _selectedIds = /* @__PURE__ */ new Set();
  async function loadImages() {
    try {
      var resp = await fetch("/api/images");
      if (!resp.ok) throw new Error("HTTP " + resp.status);
      var data = await resp.json();
      _allImages = data.images || [];
      renderImagesTable();
    } catch (err) {
      console.error("Failed to load images:", err);
    }
  }
  function filterImages(filter) {
    _currentFilter = filter;
    var pills = document.querySelectorAll(".images-filter-pill");
    for (var i = 0; i < pills.length; i++) {
      pills[i].classList.toggle("active", pills[i].getAttribute("data-filter") === filter);
    }
    renderImagesTable();
  }
  function sortImages(sort) {
    _currentSort = sort;
    var pills = document.querySelectorAll(".images-sort-pill");
    for (var i = 0; i < pills.length; i++) {
      pills[i].classList.toggle("active", pills[i].getAttribute("data-sort") === sort);
    }
    renderImagesTable();
  }
  function getFilteredAndSorted() {
    var filtered = _allImages;
    if (_currentFilter === "in-use") {
      filtered = _allImages.filter(function(img) {
        return img.in_use;
      });
    } else if (_currentFilter === "unused") {
      filtered = _allImages.filter(function(img) {
        return !img.in_use;
      });
    }
    filtered = filtered.slice();
    if (_currentSort === "alpha") {
      filtered.sort(function(a, b) {
        var tagA = a.repo_tags && a.repo_tags.length > 0 ? a.repo_tags[0].toLowerCase() : "zzz";
        var tagB = b.repo_tags && b.repo_tags.length > 0 ? b.repo_tags[0].toLowerCase() : "zzz";
        return tagA < tagB ? -1 : tagA > tagB ? 1 : 0;
      });
    } else {
      filtered.sort(function(a, b) {
        if (a.in_use !== b.in_use) return a.in_use ? -1 : 1;
        return b.created - a.created;
      });
    }
    return filtered;
  }
  function toggleManageMode2() {
    _manageMode = !_manageMode;
    _selectedIds.clear();
    var btn = document.getElementById("manage-btn");
    if (btn) btn.textContent = _manageMode ? "Cancel" : "Manage";
    var table = document.querySelector(".table-images");
    if (table) table.classList.toggle("managing", _manageMode);
    filterImages(_manageMode ? "unused" : "all");
    updateBulkBar();
  }
  function updateBulkBar() {
    var bar = document.getElementById("images-bulk-bar");
    if (!bar) return;
    if (_manageMode && _selectedIds.size > 0) {
      bar.style.display = "flex";
      var countEl = bar.querySelector(".bulk-count");
      if (countEl) countEl.textContent = _selectedIds.size + " selected";
    } else {
      bar.style.display = "none";
    }
  }
  function toggleImageSelect(id) {
    if (_selectedIds.has(id)) {
      _selectedIds.delete(id);
    } else {
      _selectedIds.add(id);
    }
    var cb = document.querySelector('input[data-image-id="' + CSS.escape(id) + '"]');
    if (cb) cb.checked = _selectedIds.has(id);
    var selectAll = document.getElementById("images-select-all");
    if (selectAll) {
      var visible = getFilteredAndSorted().filter(function(img) {
        return !img.in_use;
      });
      selectAll.checked = visible.length > 0 && visible.every(function(img) {
        return _selectedIds.has(img.id);
      });
    }
    updateBulkBar();
  }
  function toggleSelectAll() {
    var visible = getFilteredAndSorted().filter(function(img) {
      return !img.in_use;
    });
    var allSelected = visible.length > 0 && visible.every(function(img) {
      return _selectedIds.has(img.id);
    });
    if (allSelected) {
      visible.forEach(function(img) {
        _selectedIds.delete(img.id);
      });
    } else {
      visible.forEach(function(img) {
        _selectedIds.add(img.id);
      });
    }
    renderImagesTable();
    updateBulkBar();
  }
  var _deleting = false;
  async function removeSelectedImages() {
    var count = _selectedIds.size;
    if (count === 0 || _deleting) return;
    if (!confirm("Remove " + count + " selected image" + (count > 1 ? "s" : "") + "? This cannot be undone.")) return;
    _deleting = true;
    var removeBtn = document.querySelector("#images-bulk-bar .btn-danger");
    if (removeBtn) removeBtn.disabled = true;
    var ids = Array.from(_selectedIds);
    var removed = 0;
    var failed = 0;
    for (var i = 0; i < ids.length; i++) {
      try {
        var resp = await fetch("/api/images/" + encodeURIComponent(ids[i]), { method: "DELETE" });
        if (resp.ok) {
          removed++;
        } else {
          failed++;
        }
      } catch (_) {
        failed++;
      }
    }
    _deleting = false;
    if (removeBtn) removeBtn.disabled = false;
    if (window.showToast) {
      if (failed > 0) {
        window.showToast("Removed " + removed + ", failed " + failed, "warning");
      } else {
        window.showToast("Removed " + removed + " image" + (removed > 1 ? "s" : ""));
      }
    }
    _selectedIds.clear();
    _manageMode = false;
    var btn = document.getElementById("manage-btn");
    if (btn) btn.textContent = "Manage";
    updateBulkBar();
    loadImages();
  }
  function renderImagesTable() {
    var tbody = document.getElementById("images-tbody");
    if (!tbody) return;
    while (tbody.firstChild) tbody.removeChild(tbody.firstChild);
    var images = getFilteredAndSorted();
    if (images.length === 0) {
      var emptyRow = document.createElement("tr");
      var emptyCell = document.createElement("td");
      emptyCell.colSpan = _manageMode ? 7 : 6;
      emptyCell.style.textAlign = "center";
      emptyCell.style.padding = "2rem";
      emptyCell.style.color = "var(--text-secondary)";
      emptyCell.textContent = _currentFilter !== "all" ? "No " + _currentFilter + " images" : "No images found";
      emptyRow.appendChild(emptyCell);
      tbody.appendChild(emptyRow);
      return;
    }
    var totalSize = _allImages.reduce(function(sum, img2) {
      return sum + img2.size;
    }, 0);
    var inUse = _allImages.filter(function(img2) {
      return img2.in_use;
    }).length;
    var summaryEl = document.getElementById("images-summary");
    if (summaryEl) {
      summaryEl.textContent = _allImages.length + " images (" + inUse + " in use), " + formatBytes(totalSize) + " total";
    }
    var thead = tbody.closest("table").querySelector("thead tr");
    var existingTh = thead ? thead.querySelector(".th-select") : null;
    if (_manageMode && !existingTh && thead) {
      var th = document.createElement("th");
      th.className = "th-select";
      th.style.width = "40px";
      var selectAll = document.createElement("input");
      selectAll.type = "checkbox";
      selectAll.id = "images-select-all";
      selectAll.title = "Select all unused";
      selectAll.addEventListener("change", function() {
        toggleSelectAll();
      });
      th.appendChild(selectAll);
      thead.insertBefore(th, thead.firstChild);
    } else if (!_manageMode && existingTh) {
      existingTh.remove();
    }
    for (var i = 0; i < images.length; i++) {
      var img = images[i];
      var row = document.createElement("tr");
      if (_manageMode) {
        var checkCell = document.createElement("td");
        checkCell.className = "td-checkbox";
        if (!img.in_use) {
          var cb = document.createElement("input");
          cb.type = "checkbox";
          cb.checked = _selectedIds.has(img.id);
          cb.setAttribute("data-image-id", img.id);
          cb.addEventListener("change", /* @__PURE__ */ (function(id) {
            return function() {
              toggleImageSelect(id);
            };
          })(img.id));
          checkCell.appendChild(cb);
        }
        row.appendChild(checkCell);
      }
      var tagsCell = document.createElement("td");
      tagsCell.className = "cell-image-tags";
      if (img.repo_tags && img.repo_tags.length > 0) {
        for (var t = 0; t < img.repo_tags.length; t++) {
          var tagSpan = document.createElement("code");
          tagSpan.className = "image-tag";
          tagSpan.textContent = img.repo_tags[t];
          tagSpan.title = img.repo_tags[t];
          tagsCell.appendChild(tagSpan);
          if (t < img.repo_tags.length - 1) {
            tagsCell.appendChild(document.createElement("br"));
          }
        }
      } else {
        var noneSpan = document.createElement("span");
        noneSpan.className = "text-muted";
        noneSpan.textContent = "<none>";
        tagsCell.appendChild(noneSpan);
      }
      row.appendChild(tagsCell);
      var idCell = document.createElement("td");
      idCell.className = "cell-image-id";
      var code = document.createElement("code");
      code.title = img.id;
      code.textContent = img.id.replace("sha256:", "").substring(0, 12);
      idCell.appendChild(code);
      row.appendChild(idCell);
      var sizeCell = document.createElement("td");
      sizeCell.className = "cell-image-size";
      sizeCell.textContent = formatBytes(img.size);
      row.appendChild(sizeCell);
      var createdCell = document.createElement("td");
      createdCell.title = new Date(img.created * 1e3).toISOString();
      createdCell.textContent = formatRelativeTime(img.created);
      row.appendChild(createdCell);
      var useCell = document.createElement("td");
      var badge = document.createElement("span");
      badge.className = img.in_use ? "badge badge-success" : "badge badge-muted";
      badge.textContent = img.in_use ? "In Use" : "Unused";
      useCell.appendChild(badge);
      row.appendChild(useCell);
      var actionsCell = document.createElement("td");
      if (!_manageMode) {
        var btn = document.createElement("button");
        btn.className = "btn btn-sm btn-danger";
        btn.textContent = "Remove";
        if (img.in_use) {
          btn.disabled = true;
          btn.title = "Cannot remove: image is in use by a container";
        } else {
          btn.setAttribute("data-image-id", img.id);
          btn.addEventListener("click", /* @__PURE__ */ (function(imageId) {
            return function() {
              removeImage(imageId);
            };
          })(img.id));
        }
        actionsCell.appendChild(btn);
      }
      row.appendChild(actionsCell);
      tbody.appendChild(row);
    }
  }
  function formatBytes(bytes) {
    if (bytes === 0) return "0 B";
    var k = 1024;
    var sizes = ["B", "KB", "MB", "GB", "TB"];
    var i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + " " + sizes[i];
  }
  function formatRelativeTime(unixSeconds) {
    var now = Date.now() / 1e3;
    var diff = now - unixSeconds;
    if (diff < 60) return "just now";
    if (diff < 3600) return Math.floor(diff / 60) + "m ago";
    if (diff < 86400) return Math.floor(diff / 3600) + "h ago";
    return Math.floor(diff / 86400) + "d ago";
  }
  async function pruneImages() {
    if (!confirm("Remove all dangling (unused, untagged) images?")) return;
    try {
      var resp = await fetch("/api/images/prune", { method: "POST" });
      if (!resp.ok) throw new Error("HTTP " + resp.status);
      var data = await resp.json();
      if (window.showToast) {
        window.showToast("Pruned " + data.images_deleted + " images, reclaimed " + formatBytes(data.space_reclaimed));
      }
      loadImages();
    } catch (err) {
      if (window.showToast) window.showToast("Prune failed: " + err.message, "error");
    }
  }
  async function removeImage(id) {
    if (!confirm("Remove this image? This cannot be undone.")) return;
    try {
      var resp = await fetch("/api/images/" + encodeURIComponent(id), { method: "DELETE" });
      if (!resp.ok) throw new Error("HTTP " + resp.status);
      if (window.showToast) window.showToast("Image removed");
      loadImages();
    } catch (err) {
      if (window.showToast) window.showToast("Remove failed: " + err.message, "error");
    }
  }

  // internal/web/static/src/js/logs.js
  var _allLogs = [];
  var _currentType = "all";
  var TYPE_GROUPS = {
    update: [
      "update",
      "rollback",
      "approve",
      "reject",
      "ignore",
      "check",
      "self_update",
      "update_to_version",
      "restart",
      "start",
      "stop",
      "scale",
      "scan",
      "webhook",
      "ghcr_switch",
      "image_prune",
      "image_remove"
    ],
    policy: ["policy_set", "policy_delete", "bulk_policy", "notify_pref", "notify_states_cleared"],
    auth: ["auth"],
    settings: ["settings", "cluster-settings", "config-import", "digest", "hooks"]
  };
  var TYPE_BADGE = {
    policy_set: "badge-info",
    policy_delete: "badge-muted",
    approve: "badge-success",
    reject: "badge-error",
    update: "badge-success",
    rollback: "badge-warning",
    start: "badge-success",
    stop: "badge-error",
    restart: "badge-warning",
    auth: "badge-info",
    settings: "badge-muted",
    scan: "badge-info",
    check: "badge-info"
  };
  async function loadActivityLogs() {
    try {
      var resp = await fetch("/api/logs");
      if (!resp.ok) throw new Error("HTTP " + resp.status);
      _allLogs = await resp.json();
      if (!Array.isArray(_allLogs)) _allLogs = [];
      renderLogs();
    } catch (err) {
      console.error("Failed to load logs:", err);
    }
  }
  function filterLogs(type) {
    _currentType = type;
    var pills = document.querySelectorAll(".logs-type-pill");
    for (var i = 0; i < pills.length; i++) {
      pills[i].classList.toggle("active", pills[i].getAttribute("data-type") === type);
    }
    renderLogs();
  }
  function getFiltered() {
    if (_currentType === "all") return _allLogs;
    var types = TYPE_GROUPS[_currentType] || [];
    return _allLogs.filter(function(log) {
      return types.indexOf(log.type) >= 0;
    });
  }
  function renderLogs() {
    var tbody = document.getElementById("logs-tbody");
    if (!tbody) return;
    while (tbody.firstChild) tbody.removeChild(tbody.firstChild);
    var logs = getFiltered();
    var summary = document.getElementById("logs-summary");
    if (summary) {
      var text = _allLogs.length + " total entries";
      if (_currentType !== "all") text += " (" + logs.length + " shown)";
      summary.textContent = text;
    }
    if (logs.length === 0) {
      var tr = document.createElement("tr");
      var td = document.createElement("td");
      td.colSpan = 5;
      td.style.textAlign = "center";
      td.style.padding = "2rem";
      td.style.color = "var(--text-secondary)";
      td.textContent = _currentType !== "all" ? "No " + _currentType + " entries" : "No activity logged yet.";
      tr.appendChild(td);
      tbody.appendChild(tr);
      return;
    }
    for (var i = 0; i < logs.length; i++) {
      var log = logs[i];
      var row = document.createElement("tr");
      var timeCell = document.createElement("td");
      var ts = new Date(log.timestamp);
      timeCell.title = ts.toISOString();
      timeCell.textContent = formatTimeAgo(ts);
      row.appendChild(timeCell);
      var userCell = document.createElement("td");
      if (log.user) {
        userCell.textContent = log.user;
      } else {
        var sys = document.createElement("span");
        sys.className = "text-muted";
        sys.textContent = "system";
        userCell.appendChild(sys);
      }
      row.appendChild(userCell);
      var typeCell = document.createElement("td");
      var badge = document.createElement("span");
      badge.className = "badge " + (TYPE_BADGE[log.type] || "badge-muted");
      badge.textContent = log.type;
      typeCell.appendChild(badge);
      row.appendChild(typeCell);
      var containerCell = document.createElement("td");
      containerCell.className = "mono";
      if (log.container) {
        var link = document.createElement("a");
        link.href = (log.kind === "service" ? "/service/" : "/container/") + encodeURIComponent(log.container);
        link.textContent = log.container;
        containerCell.appendChild(link);
      } else {
        containerCell.textContent = "-";
      }
      row.appendChild(containerCell);
      var msgCell = document.createElement("td");
      msgCell.title = log.message;
      msgCell.textContent = log.message;
      row.appendChild(msgCell);
      tbody.appendChild(row);
    }
  }
  function exportLogs(format) {
    var logs = getFiltered();
    if (logs.length === 0) {
      if (window.showToast) window.showToast("No logs to export", "warning");
      return;
    }
    var content, filename, mime;
    if (format === "json") {
      content = JSON.stringify(logs, null, 2);
      filename = "sentinel-logs.json";
      mime = "application/json";
    } else {
      var rows = ["Time,User,Type,Container,Message"];
      for (var i = 0; i < logs.length; i++) {
        var l = logs[i];
        rows.push([
          new Date(l.timestamp).toISOString(),
          csvEscape(l.user || "system"),
          csvEscape(l.type),
          csvEscape(l.container || ""),
          csvEscape(l.message)
        ].join(","));
      }
      content = rows.join("\n");
      filename = "sentinel-logs.csv";
      mime = "text/csv";
    }
    var blob = new Blob([content], { type: mime });
    var url = URL.createObjectURL(blob);
    var a = document.createElement("a");
    a.href = url;
    a.download = filename;
    a.click();
    URL.revokeObjectURL(url);
  }
  function csvEscape(str) {
    if (!str) return "";
    if (str.indexOf(",") >= 0 || str.indexOf('"') >= 0 || str.indexOf("\n") >= 0) {
      return '"' + str.replace(/"/g, '""') + '"';
    }
    return str;
  }
  function formatTimeAgo(date) {
    var diff = (Date.now() - date.getTime()) / 1e3;
    if (diff < 60) return "just now";
    if (diff < 3600) return Math.floor(diff / 60) + "m ago";
    if (diff < 86400) return Math.floor(diff / 3600) + "h ago";
    if (diff < 604800) return Math.floor(diff / 86400) + "d ago";
    return date.toLocaleDateString();
  }

  // internal/web/static/src/js/main.js
  setUpdateStatsFn(updateStats2);
  window._dashboardSelectedContainers = selectedContainers;
  window._dashboardManageMode = false;
  window._queueBulkInProgress = getBulkInProgress;
  window.showToast = showToast;
  window.csrfToken = getCSRFToken;
  window.escapeHTML = escapeHTML;
  window.showConfirm = showConfirm;
  window.apiPost = apiPost;
  window.activateFilter = activateFilter;
  window.resumeScanning = resumeScanning;
  window.expandAllStacks = expandAllStacks;
  window.collapseAllStacks = collapseAllStacks;
  window.toggleManageMode = function() {
    toggleManageMode();
    window._dashboardManageMode = manageMode;
  };
  window.triggerScan = triggerScan;
  window.toggleHostGroup = toggleHostGroup;
  window.toggleStack = toggleStack;
  window.toggleSwarmSection = toggleSwarmSection;
  window.onRowClick = onRowClick;
  window.applyBulkPolicy = applyBulkPolicy;
  window.clearSelection = clearSelection;
  window.applyTheme = applyTheme;
  window.applyFiltersAndSort = applyFiltersAndSort;
  window.recalcTabStats = recalcTabStats;
  window.recomputeSelectionState = recomputeSelectionState;
  window.checkPauseState = checkPauseState;
  window.refreshLastScan = refreshLastScan;
  window.fetchContainerLogs = fetchContainerLogs;
  window.toggleQueueAccordion = toggleQueueAccordion;
  window.approveUpdate = approveUpdate;
  window.ignoreUpdate = ignoreUpdate;
  window.rejectUpdate = rejectUpdate;
  window.approveAll = approveAll;
  window.ignoreAll = ignoreAll;
  window.rejectAll = rejectAll;
  window.triggerUpdate = triggerUpdate;
  window.triggerCheck = triggerCheck;
  window.triggerRollback = triggerRollback;
  window.changePolicy = changePolicy;
  window.triggerSelfUpdate = triggerSelfUpdate;
  window.loadAllTags = loadAllTags;
  window.updateToVersion = updateToVersion;
  window.switchToGHCR = switchToGHCR;
  window.toggleSvc = toggleSvc;
  window.triggerSvcUpdate = triggerSvcUpdate;
  window.changeSvcPolicy = changeSvcPolicy;
  window.rollbackSvc = rollbackSvc;
  window.scaleSvc = scaleSvc;
  window.refreshServiceRow = refreshServiceRow;
  window.updateContainerRow = updateContainerRow;
  window.updateQueueBadge = updateQueueBadge;
  window.applyRegistryBadges = applyRegistryBadges;
  window.loadGHCRAlternatives = loadGHCRAlternatives;
  window.renderGHCRAlternatives = renderGHCRAlternatives;
  window.onPollIntervalChange = onPollIntervalChange;
  window.onCustomUnitChange = onCustomUnitChange;
  window.applyCustomPollInterval = applyCustomPollInterval;
  window.setDefaultPolicy = setDefaultPolicy;
  window.setRollbackPolicy = setRollbackPolicy;
  window.onGracePeriodChange = onGracePeriodChange;
  window.applyCustomGracePeriod = applyCustomGracePeriod;
  window.setLatestAutoUpdate = setLatestAutoUpdate;
  window.setPauseState = setPauseState;
  window.saveFilters = saveFilters;
  window.setImageCleanup = setImageCleanup;
  window.saveCronSchedule = saveCronSchedule;
  window.setDependencyAware = setDependencyAware;
  window.setHooksEnabled = setHooksEnabled;
  window.setHooksWriteLabels = setHooksWriteLabels;
  window.setDryRun = setDryRun;
  window.setPullOnly = setPullOnly;
  window.setUpdateDelay = setUpdateDelay;
  window.setComposeSync = setComposeSync;
  window.setImageBackup = setImageBackup;
  window.setShowStopped = setShowStopped;
  window.setRemoveVolumes = setRemoveVolumes;
  window.setScanConcurrency = setScanConcurrency;
  window.setHADiscovery = setHADiscovery;
  window.saveHADiscoveryPrefix = saveHADiscoveryPrefix;
  window.updateToggleText = updateToggleText;
  window.toggleCollapsible = toggleCollapsible;
  window.saveDockerTLS = saveDockerTLS;
  window.testDockerTLS = testDockerTLS;
  window.setWebhookEnabled = setWebhookEnabled;
  window.regenerateWebhookSecret = regenerateWebhookSecret;
  window.copyWebhookURL = copyWebhookURL;
  window.copyWebhookSecret = copyWebhookSecret;
  window.saveMaintenanceWindow = saveMaintenanceWindow;
  window.exportConfig = exportConfig;
  window.importConfig = importConfig;
  window.onClusterToggle = onClusterToggle;
  window.saveClusterSettings = saveClusterSettings;
  window.loadClusterSettings = loadClusterSettings;
  window.addChannel = addChannel;
  window.saveNotificationChannels = saveNotificationChannels;
  window.testNotification = testNotification;
  window.onNotifyModeChange = onNotifyModeChange;
  window.saveDigestSettings = saveDigestSettings;
  window.triggerDigest = triggerDigest;
  window.saveNotifyPref = setContainerNotifyPref;
  window.loadNotificationChannels = loadNotificationChannels;
  window.loadDigestSettings = loadDigestSettings;
  window.loadContainerNotifyPrefs = loadContainerNotifyPrefs;
  window.loadNotifyTemplates = loadNotifyTemplates;
  window.loadTemplateForEvent = loadTemplateForEvent;
  window.saveNotifyTemplate = saveNotifyTemplate;
  window.deleteNotifyTemplate = deleteNotifyTemplate;
  window.previewNotifyTemplate = previewNotifyTemplate;
  window.addRegistryCredential = addRegistryCredential;
  window.saveRegistryCredentials = saveRegistryCredentials;
  window.loadRegistries = loadRegistries;
  window.addReleaseSource = addReleaseSource;
  window.saveReleaseSources = saveReleaseSources;
  window.loadAboutInfo = loadAboutInfo;
  window.loadReleaseSources = loadReleaseSources;
  window.loadImages = loadImages;
  window.pruneImages = pruneImages;
  window.removeImage = removeImage;
  window.filterImages = filterImages;
  window.sortImages = sortImages;
  window.toggleImageManageMode = toggleManageMode2;
  window.toggleImageSelect = toggleImageSelect;
  window.toggleImageSelectAll = toggleSelectAll;
  window.removeSelectedImages = removeSelectedImages;
  window.loadActivityLogs = loadActivityLogs;
  window.filterLogs = filterLogs;
  window.exportLogs = exportLogs;
  document.addEventListener("DOMContentLoaded", function() {
    initTheme();
    var path = window.location.pathname;
    if (path !== "/login" && path !== "/setup") {
      initSSE();
    }
    initPauseBanner();
    loadFooterVersion();
    loadDigestBanner();
    initFilters();
    initDashboardTabs();
    refreshLastScan();
    var stackPref = localStorage.getItem("sentinel-stacks") || "collapsed";
    if (stackPref === "expanded") {
      expandAllStacks();
    }
    initSettingsPage();
    initAccordionPersistence();
    var stats = document.getElementById("stats");
    if (stats) {
      var pendingEl = stats.querySelectorAll(".stat-value")[2];
      if (pendingEl) {
        var val = parseInt(pendingEl.textContent.trim(), 10);
        updatePendingColor(val);
      }
    }
    var stackHeaders = document.querySelectorAll(".stack-header");
    for (var sh = 0; sh < stackHeaders.length; sh++) {
      stackHeaders[sh].setAttribute("tabindex", "0");
      stackHeaders[sh].setAttribute("role", "button");
      stackHeaders[sh].setAttribute("aria-expanded", "false");
      stackHeaders[sh].addEventListener("keydown", function(e) {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          toggleStack(this);
        }
      });
    }
    var policyHelps = document.querySelectorAll(".policy-help");
    for (var ph = 0; ph < policyHelps.length; ph++) {
      policyHelps[ph].setAttribute("role", "button");
      policyHelps[ph].setAttribute("aria-label", "Policy information");
      policyHelps[ph].addEventListener("keydown", function(e) {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          var tooltip = this.querySelector(".policy-tooltip");
          if (tooltip) {
            var isVisible = tooltip.style.display === "block";
            tooltip.style.display = isVisible ? "" : "block";
          }
        }
      });
    }
    document.addEventListener("keydown", function(e) {
      if (e.key === "Escape") {
        var tooltips = document.querySelectorAll(".policy-tooltip");
        for (var t = 0; t < tooltips.length; t++) {
          tooltips[t].style.display = "";
        }
      }
    });
    var table = document.getElementById("container-table");
    if (table) {
      table.addEventListener("change", function(e) {
        var target = e.target;
        if (target.id === "select-all") {
          var checkboxes = table.querySelectorAll(".row-select");
          for (var i = 0; i < checkboxes.length; i++) {
            checkboxes[i].checked = target.checked;
            selectedContainers[checkboxes[i].value] = target.checked;
          }
          recomputeSelectionState();
          return;
        }
        if (target.classList.contains("stack-select")) {
          var tbody = target.closest("tbody.stack-group");
          if (tbody) {
            var rows = tbody.querySelectorAll(".row-select");
            for (var i = 0; i < rows.length; i++) {
              rows[i].checked = target.checked;
              selectedContainers[rows[i].value] = target.checked;
            }
          }
          recomputeSelectionState();
          return;
        }
        if (target.classList.contains("row-select")) {
          selectedContainers[target.value] = target.checked;
          recomputeSelectionState();
        }
      });
      table.addEventListener("click", function(e) {
        if (e.target.classList.contains("stack-select")) {
          e.stopPropagation();
        }
      });
      applyRegistryBadges();
      loadGHCRAlternatives();
    }
    document.addEventListener("click", function(e) {
      var badge = e.target.closest(".status-badge-wrap .badge-hover");
      if (!badge) return;
      e.stopPropagation();
      var wrap = badge.closest(".status-badge-wrap");
      if (!wrap) return;
      var name = wrap.getAttribute("data-name");
      if (!name) return;
      var action = wrap.getAttribute("data-action") || "restart";
      var hostId = wrap.getAttribute("data-host-id");
      var actionKey = (hostId || "") + "::" + name;
      pendingBadgeActions[actionKey] = true;
      showBadgeSpinner2(wrap);
      var endpoint = "/api/containers/" + encodeURIComponent(name) + "/" + action;
      if (hostId) endpoint += "?host=" + encodeURIComponent(hostId);
      var label = action.charAt(0).toUpperCase() + action.slice(1);
      apiPost(
        endpoint,
        null,
        label + " initiated for " + name,
        "Failed to " + action + " " + name
      );
    });
  });
})();
//# sourceMappingURL=app.js.map
