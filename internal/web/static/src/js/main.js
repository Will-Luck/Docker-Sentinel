/* ============================================================
   Docker-Sentinel Dashboard v3 - Client-side JavaScript
   ES module entry point (bundled by esbuild --format=iife)
   ============================================================ */

// Side-effect import: patches window.fetch with CSRF token injection.
import { getCSRFToken } from "./csrf.js";

import {
    showToast,
    escapeHTML,
    showConfirm,
    apiPost
} from "./utils.js";

import {
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
    toggleManageMode,
    fetchContainerLogs
} from "./dashboard.js";

import {
    toggleQueueAccordion,
    approveUpdate,
    ignoreUpdate,
    rejectUpdate,
    getBulkInProgress,
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
} from "./queue.js";

import {
    toggleSvc,
    triggerSvcUpdate,
    changeSvcPolicy,
    rollbackSvc,
    scaleSvc,
    refreshServiceRow
} from "./swarm.js";

import {
    initSSE,
    updateContainerRow,
    updateStats,
    updatePendingColor,
    showBadgeSpinner,
    reapplyBadgeSpinners,
    clearPendingBadge,
    pendingBadgeActions,
    loadGHCRAlternatives,
    applyRegistryBadges,
    loadDigestBanner,
    scheduleDigestBannerRefresh,
    updateQueueBadge,
    dismissDigestBanner,
    renderGHCRAlternatives,
    refreshDashboardStats
} from "./sse.js";

import {
    initSettingsPage,
    onPollIntervalChange,
    onCustomUnitChange,
    applyCustomPollInterval,
    setDefaultPolicy,
    setRollbackPolicy,
    onGracePeriodChange,
    applyCustomGracePeriod,
    setLatestAutoUpdate,
    setPauseState,
    saveFilters,
    setImageCleanup,
    saveCronSchedule,
    setDependencyAware,
    setHooksEnabled,
    setHooksWriteLabels,
    setDryRun,
    setPullOnly,
    setUpdateDelay,
    setComposeSync,
    setImageBackup,
    setShowStopped,
    setRemoveVolumes,
    setScanConcurrency,
    setHADiscovery,
    saveHADiscoveryPrefix,
    updateToggleText,
    toggleCollapsible,
    saveDockerTLS,
    testDockerTLS,
    setWebhookEnabled,
    regenerateWebhookSecret,
    copyWebhookURL,
    copyWebhookSecret,
    saveMaintenanceWindow,
    exportConfig,
    importConfig
} from "./settings-core.js";

import {
    loadClusterSettings,
    onClusterToggle,
    saveClusterSettings
} from "./settings-cluster.js";

import {
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
    loadNotifyTemplates,
    loadTemplateForEvent,
    saveNotifyTemplate,
    deleteNotifyTemplate,
    previewNotifyTemplate
} from "./notifications.js";

import {
    loadRegistries,
    addRegistryCredential,
    saveRegistryCredentials,
    updateRateLimitStatus
} from "./registries.js";

import {
    loadFooterVersion,
    loadAboutInfo,
    loadReleaseSources,
    addReleaseSource,
    saveReleaseSources
} from "./about.js";

import {
    loadImages,
    pruneImages,
    removeImage,
    filterImages,
    sortImages,
    toggleManageMode as toggleImageManageMode,
    toggleImageSelect,
    toggleSelectAll as toggleImageSelectAll,
    removeSelectedImages
} from "./images.js";

import {
    loadActivityLogs,
    filterLogs,
    exportLogs
} from "./logs.js";

// Wire updateStats into dashboard module (avoids circular import).
setUpdateStatsFn(updateStats);

// Expose dashboard state for cross-module access via window.
// Queue and SSE modules read these to avoid circular imports.
window._dashboardSelectedContainers = selectedContainers;
window._dashboardManageMode = false;

// Expose getBulkInProgress for SSE module.
window._queueBulkInProgress = getBulkInProgress;

/* ------------------------------------------------------------
   Window exports - functions called from HTML onclick/onchange
   ------------------------------------------------------------ */

// Utils (used by inline scripts)
window.showToast = showToast;
window.csrfToken = getCSRFToken;
window.escapeHTML = escapeHTML;
window.showConfirm = showConfirm;
window.apiPost = apiPost;

// Dashboard
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

// Queue
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

// Swarm
window.toggleSvc = toggleSvc;
window.triggerSvcUpdate = triggerSvcUpdate;
window.changeSvcPolicy = changeSvcPolicy;
window.rollbackSvc = rollbackSvc;
window.scaleSvc = scaleSvc;
window.refreshServiceRow = refreshServiceRow;

// SSE
window.updateContainerRow = updateContainerRow;
window.updateQueueBadge = updateQueueBadge;
window.applyRegistryBadges = applyRegistryBadges;
window.loadGHCRAlternatives = loadGHCRAlternatives;
window.renderGHCRAlternatives = renderGHCRAlternatives;

// Settings core
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

// Settings cluster
window.onClusterToggle = onClusterToggle;
window.saveClusterSettings = saveClusterSettings;
window.loadClusterSettings = loadClusterSettings;

// Notifications
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

// Registries
window.addRegistryCredential = addRegistryCredential;
window.saveRegistryCredentials = saveRegistryCredentials;
window.loadRegistries = loadRegistries;

// About
window.addReleaseSource = addReleaseSource;
window.saveReleaseSources = saveReleaseSources;
window.loadAboutInfo = loadAboutInfo;
window.loadReleaseSources = loadReleaseSources;

// Images
window.loadImages = loadImages;
window.pruneImages = pruneImages;
window.removeImage = removeImage;
window.filterImages = filterImages;
window.sortImages = sortImages;
window.toggleImageManageMode = toggleImageManageMode;
window.toggleImageSelect = toggleImageSelect;
window.toggleImageSelectAll = toggleImageSelectAll;
window.removeSelectedImages = removeSelectedImages;

// Activity Logs
window.loadActivityLogs = loadActivityLogs;
window.filterLogs = filterLogs;
window.exportLogs = exportLogs;

/* ------------------------------------------------------------
   12. Initialisation
   ------------------------------------------------------------ */

document.addEventListener("DOMContentLoaded", function () {
    initTheme();
    // Only init SSE on authenticated pages (skip login/setup).
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

    // Apply stack default preference.
    var stackPref = localStorage.getItem("sentinel-stacks") || "collapsed";
    if (stackPref === "expanded") {
        expandAllStacks();
    }

    initSettingsPage();
    initAccordionPersistence();

    // Color the pending stat card based on initial value.
    var stats = document.getElementById("stats");
    if (stats) {
        var pendingEl = stats.querySelectorAll(".stat-value")[2];
        if (pendingEl) {
            var val = parseInt(pendingEl.textContent.trim(), 10);
            updatePendingColor(val);
        }
    }

    // Keyboard support for stack headers.
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

    // Keyboard support for policy help tooltips.
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


    // Escape key closes open tooltips.
    document.addEventListener("keydown", function(e) {
        if (e.key === "Escape") {
            var tooltips = document.querySelectorAll(".policy-tooltip");
            for (var t = 0; t < tooltips.length; t++) {
                tooltips[t].style.display = "";
            }
        }
    });

    // Multi-select: checkbox delegation on container table.
    var table = document.getElementById("container-table");
    if (table) {
        table.addEventListener("change", function (e) {
            var target = e.target;

            // Select-all checkbox
            if (target.id === "select-all") {
                var checkboxes = table.querySelectorAll(".row-select");
                for (var i = 0; i < checkboxes.length; i++) {
                    checkboxes[i].checked = target.checked;
                    selectedContainers[checkboxes[i].value] = target.checked;
                }
                recomputeSelectionState();
                return;
            }

            // Stack-level select checkbox
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

            // Individual row checkbox
            if (target.classList.contains("row-select")) {
                selectedContainers[target.value] = target.checked;
                recomputeSelectionState();
            }
        });

        // Prevent stack-select checkbox from toggling stack collapse.
        table.addEventListener("click", function(e) {
            if (e.target.classList.contains("stack-select")) {
                e.stopPropagation();
            }
        });

        // Apply registry source badges and load GHCR alternatives.
        applyRegistryBadges();
        loadGHCRAlternatives();
    }

    // Stop/Start/Restart badge click delegation (containers only).
    document.addEventListener("click", function(e) {
        var badge = e.target.closest(".status-badge-wrap .badge-hover");
        if (!badge) return;
        e.stopPropagation();
        var wrap = badge.closest(".status-badge-wrap");
        if (!wrap) return;

        // Container stop/start/restart actions.
        var name = wrap.getAttribute("data-name");
        if (!name) return;
        var action = wrap.getAttribute("data-action") || "restart";

        var hostId = wrap.getAttribute("data-host-id");

        // Track pending action so spinner survives DOM updates on other rows.
        var actionKey = (hostId || "") + "::" + name;
        pendingBadgeActions[actionKey] = true;

        showBadgeSpinner(wrap);
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
