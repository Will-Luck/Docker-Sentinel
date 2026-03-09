/* ============================================================
   Docker-Sentinel — Activity Logs page (client-side)
   ============================================================ */

var _allLogs = [];
var _currentType = 'all'; // all | update | policy | auth | settings

// Type groups for filtering.
var TYPE_GROUPS = {
    update:   ['update', 'rollback', 'approve', 'reject', 'ignore', 'check',
               'self_update', 'update_to_version', 'restart', 'start', 'stop',
               'scale', 'scan', 'webhook', 'ghcr_switch', 'image_prune', 'image_remove'],
    policy:   ['policy_set', 'policy_delete', 'bulk_policy', 'notify_pref', 'notify_states_cleared'],
    auth:     ['auth'],
    settings: ['settings', 'cluster-settings', 'config-import', 'digest', 'hooks']
};

// Badge colour per type.
var TYPE_BADGE = {
    policy_set:    'badge-info',
    policy_delete: 'badge-muted',
    approve:       'badge-success',
    reject:        'badge-error',
    update:        'badge-success',
    rollback:      'badge-warning',
    start:         'badge-success',
    stop:          'badge-error',
    restart:       'badge-warning',
    auth:          'badge-info',
    settings:      'badge-muted',
    scan:          'badge-info',
    check:         'badge-info'
};

export async function loadActivityLogs() {
    try {
        var resp = await fetch('/api/logs');
        if (!resp.ok) throw new Error('HTTP ' + resp.status);
        _allLogs = await resp.json();
        if (!Array.isArray(_allLogs)) _allLogs = [];
        renderLogs();
    } catch (err) {
        console.error('Failed to load logs:', err);
    }
}

export function filterLogs(type) {
    _currentType = type;
    var pills = document.querySelectorAll('.logs-type-pill');
    for (var i = 0; i < pills.length; i++) {
        pills[i].classList.toggle('active', pills[i].getAttribute('data-type') === type);
    }
    renderLogs();
}

function getFiltered() {
    if (_currentType === 'all') return _allLogs;
    var types = TYPE_GROUPS[_currentType] || [];
    return _allLogs.filter(function(log) {
        return types.indexOf(log.type) >= 0;
    });
}

function renderLogs() {
    var tbody = document.getElementById('logs-tbody');
    if (!tbody) return;
    while (tbody.firstChild) tbody.removeChild(tbody.firstChild);

    var logs = getFiltered();

    // Update summary.
    var summary = document.getElementById('logs-summary');
    if (summary) {
        var text = _allLogs.length + ' total entries';
        if (_currentType !== 'all') text += ' (' + logs.length + ' shown)';
        summary.textContent = text;
    }

    if (logs.length === 0) {
        var tr = document.createElement('tr');
        var td = document.createElement('td');
        td.colSpan = 5;
        var emptyDiv = document.createElement('div');
        emptyDiv.className = 'empty-state';
        var iconDiv = document.createElement('div');
        iconDiv.className = 'empty-state-icon';
        iconDiv.innerHTML = '<svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M19.5 14.25v-2.625a3.375 3.375 0 0 0-3.375-3.375h-1.5A1.125 1.125 0 0 1 13.5 7.125v-1.5a3.375 3.375 0 0 0-3.375-3.375H8.25m0 12.75h7.5m-7.5 3H12M10.5 2.25H5.625c-.621 0-1.125.504-1.125 1.125v17.25c0 .621.504 1.125 1.125 1.125h12.75c.621 0 1.125-.504 1.125-1.125V11.25a9 9 0 0 0-9-9Z"/></svg>';
        emptyDiv.appendChild(iconDiv);
        var msg = document.createElement('p');
        msg.textContent = _currentType !== 'all' ? 'No ' + _currentType + ' entries' : 'No activity logged yet.';
        emptyDiv.appendChild(msg);
        td.appendChild(emptyDiv);
        tr.appendChild(td);
        tbody.appendChild(tr);
        return;
    }

    for (var i = 0; i < logs.length; i++) {
        var log = logs[i];
        var row = document.createElement('tr');

        // Time
        var timeCell = document.createElement('td');
        var ts = new Date(log.timestamp);
        timeCell.title = ts.toISOString();
        timeCell.textContent = formatTimeAgo(ts);
        row.appendChild(timeCell);

        // User
        var userCell = document.createElement('td');
        if (log.user) {
            userCell.textContent = log.user;
        } else {
            var sys = document.createElement('span');
            sys.className = 'text-muted';
            sys.textContent = 'system';
            userCell.appendChild(sys);
        }
        row.appendChild(userCell);

        // Type badge
        var typeCell = document.createElement('td');
        var badge = document.createElement('span');
        badge.className = 'badge ' + (TYPE_BADGE[log.type] || 'badge-muted');
        badge.textContent = log.type;
        typeCell.appendChild(badge);
        row.appendChild(typeCell);

        // Container
        var containerCell = document.createElement('td');
        containerCell.className = 'mono';
        if (log.container) {
            var link = document.createElement('a');
            link.href = (log.kind === 'service' ? '/service/' : '/container/') + encodeURIComponent(log.container);
            link.textContent = log.container;
            containerCell.appendChild(link);
        } else {
            containerCell.textContent = '-';
        }
        row.appendChild(containerCell);

        // Message
        var msgCell = document.createElement('td');
        msgCell.title = log.message;
        msgCell.textContent = log.message;
        row.appendChild(msgCell);

        tbody.appendChild(row);
    }
}

export function exportLogs(format) {
    var logs = getFiltered();
    if (logs.length === 0) {
        if (window.showToast) window.showToast('No logs to export', 'warning');
        return;
    }

    var content, filename, mime;
    if (format === 'json') {
        content = JSON.stringify(logs, null, 2);
        filename = 'sentinel-logs.json';
        mime = 'application/json';
    } else {
        var rows = ['Time,User,Type,Container,Message'];
        for (var i = 0; i < logs.length; i++) {
            var l = logs[i];
            rows.push([
                new Date(l.timestamp).toISOString(),
                csvEscape(l.user || 'system'),
                csvEscape(l.type),
                csvEscape(l.container || ''),
                csvEscape(l.message)
            ].join(','));
        }
        content = rows.join('\n');
        filename = 'sentinel-logs.csv';
        mime = 'text/csv';
    }

    var blob = new Blob([content], { type: mime });
    var url = URL.createObjectURL(blob);
    var a = document.createElement('a');
    a.href = url;
    a.download = filename;
    a.click();
    URL.revokeObjectURL(url);
}

function csvEscape(str) {
    if (!str) return '';
    if (str.indexOf(',') >= 0 || str.indexOf('"') >= 0 || str.indexOf('\n') >= 0) {
        return '"' + str.replace(/"/g, '""') + '"';
    }
    return str;
}

function formatTimeAgo(date) {
    var diff = (Date.now() - date.getTime()) / 1000;
    if (diff < 60) return 'just now';
    if (diff < 3600) return Math.floor(diff / 60) + 'm ago';
    if (diff < 86400) return Math.floor(diff / 3600) + 'h ago';
    if (diff < 604800) return Math.floor(diff / 86400) + 'd ago';
    return date.toLocaleDateString();
}
