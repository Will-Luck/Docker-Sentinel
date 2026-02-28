/* ============================================================
   Docker-Sentinel — Images management page
   ============================================================ */

// Module state: cached image data and current filter/sort.
var _allImages = [];
var _currentFilter = 'all'; // all | in-use | unused
var _currentSort = 'default'; // default (in-use first, then newest) | alpha
var _manageMode = false;
var _selectedIds = new Set();

export async function loadImages() {
    try {
        var resp = await fetch('/api/images');
        if (!resp.ok) throw new Error('HTTP ' + resp.status);
        var data = await resp.json();
        _allImages = data.images || [];
        renderImagesTable();
    } catch (err) {
        console.error('Failed to load images:', err);
    }
}

// ---- Filtering and sorting ----

export function filterImages(filter) {
    _currentFilter = filter;
    // Update active pill.
    var pills = document.querySelectorAll('.images-filter-pill');
    for (var i = 0; i < pills.length; i++) {
        pills[i].classList.toggle('active', pills[i].getAttribute('data-filter') === filter);
    }
    renderImagesTable();
}

export function sortImages(sort) {
    _currentSort = sort;
    var pills = document.querySelectorAll('.images-sort-pill');
    for (var i = 0; i < pills.length; i++) {
        pills[i].classList.toggle('active', pills[i].getAttribute('data-sort') === sort);
    }
    renderImagesTable();
}

function getFilteredAndSorted() {
    // Filter.
    var filtered = _allImages;
    if (_currentFilter === 'in-use') {
        filtered = _allImages.filter(function(img) { return img.in_use; });
    } else if (_currentFilter === 'unused') {
        filtered = _allImages.filter(function(img) { return !img.in_use; });
    }

    // Sort.
    filtered = filtered.slice(); // copy before sorting
    if (_currentSort === 'alpha') {
        filtered.sort(function(a, b) {
            var tagA = (a.repo_tags && a.repo_tags.length > 0) ? a.repo_tags[0].toLowerCase() : 'zzz';
            var tagB = (b.repo_tags && b.repo_tags.length > 0) ? b.repo_tags[0].toLowerCase() : 'zzz';
            return tagA < tagB ? -1 : tagA > tagB ? 1 : 0;
        });
    } else {
        // Default: in-use first, then newest.
        filtered.sort(function(a, b) {
            if (a.in_use !== b.in_use) return a.in_use ? -1 : 1;
            return b.created - a.created;
        });
    }
    return filtered;
}

// ---- Manage mode (multi-select) ----

export function toggleManageMode() {
    _manageMode = !_manageMode;
    _selectedIds.clear();
    var btn = document.getElementById('manage-btn');
    if (btn) btn.textContent = _manageMode ? 'Cancel' : 'Manage';
    // Toggle 'managing' class so td-checkbox cells become visible.
    var table = document.querySelector('.table-images');
    if (table) table.classList.toggle('managing', _manageMode);
    // Auto-switch to Unused filter (only unused images are selectable).
    filterImages(_manageMode ? 'unused' : 'all');
    updateBulkBar();
}

function updateBulkBar() {
    var bar = document.getElementById('images-bulk-bar');
    if (!bar) return;
    if (_manageMode && _selectedIds.size > 0) {
        bar.style.display = 'flex';
        var countEl = bar.querySelector('.bulk-count');
        if (countEl) countEl.textContent = _selectedIds.size + ' selected';
    } else {
        bar.style.display = 'none';
    }
}

export function toggleImageSelect(id) {
    if (_selectedIds.has(id)) {
        _selectedIds.delete(id);
    } else {
        _selectedIds.add(id);
    }
    // Update checkbox state without full re-render.
    var cb = document.querySelector('input[data-image-id="' + CSS.escape(id) + '"]');
    if (cb) cb.checked = _selectedIds.has(id);

    // Update select-all checkbox.
    var selectAll = document.getElementById('images-select-all');
    if (selectAll) {
        var visible = getFilteredAndSorted().filter(function(img) { return !img.in_use; });
        selectAll.checked = visible.length > 0 && visible.every(function(img) { return _selectedIds.has(img.id); });
    }
    updateBulkBar();
}

export function toggleSelectAll() {
    var visible = getFilteredAndSorted().filter(function(img) { return !img.in_use; });
    var allSelected = visible.length > 0 && visible.every(function(img) { return _selectedIds.has(img.id); });
    if (allSelected) {
        visible.forEach(function(img) { _selectedIds.delete(img.id); });
    } else {
        visible.forEach(function(img) { _selectedIds.add(img.id); });
    }
    renderImagesTable();
    updateBulkBar();
}

export async function removeSelectedImages() {
    var count = _selectedIds.size;
    if (count === 0) return;
    if (!confirm('Remove ' + count + ' selected image' + (count > 1 ? 's' : '') + '? This cannot be undone.')) return;

    var ids = Array.from(_selectedIds);
    var removed = 0;
    var failed = 0;
    for (var i = 0; i < ids.length; i++) {
        try {
            var resp = await fetch('/api/images/' + encodeURIComponent(ids[i]), { method: 'DELETE' });
            if (resp.ok) { removed++; } else { failed++; }
        } catch (_) { failed++; }
    }

    if (window.showToast) {
        if (failed > 0) {
            window.showToast('Removed ' + removed + ', failed ' + failed, 'warning');
        } else {
            window.showToast('Removed ' + removed + ' image' + (removed > 1 ? 's' : ''));
        }
    }
    _selectedIds.clear();
    _manageMode = false;
    var btn = document.getElementById('manage-btn');
    if (btn) btn.textContent = 'Manage';
    updateBulkBar();
    loadImages();
}

// ---- Rendering ----

function renderImagesTable() {
    var tbody = document.getElementById('images-tbody');
    if (!tbody) return;

    while (tbody.firstChild) tbody.removeChild(tbody.firstChild);

    var images = getFilteredAndSorted();

    if (images.length === 0) {
        var emptyRow = document.createElement('tr');
        var emptyCell = document.createElement('td');
        emptyCell.colSpan = _manageMode ? 7 : 6;
        emptyCell.style.textAlign = 'center';
        emptyCell.style.padding = '2rem';
        emptyCell.style.color = 'var(--text-secondary)';
        emptyCell.textContent = _currentFilter !== 'all' ? 'No ' + _currentFilter + ' images' : 'No images found';
        emptyRow.appendChild(emptyCell);
        tbody.appendChild(emptyRow);
        return;
    }

    // Update summary from full list (not filtered).
    var totalSize = _allImages.reduce(function(sum, img) { return sum + img.size; }, 0);
    var inUse = _allImages.filter(function(img) { return img.in_use; }).length;
    var summaryEl = document.getElementById('images-summary');
    if (summaryEl) {
        summaryEl.textContent = _allImages.length + ' images (' + inUse + ' in use), ' + formatBytes(totalSize) + ' total';
    }

    // Update header row to show/hide checkbox column.
    var thead = tbody.closest('table').querySelector('thead tr');
    var existingTh = thead ? thead.querySelector('.th-select') : null;
    if (_manageMode && !existingTh && thead) {
        var th = document.createElement('th');
        th.className = 'th-select';
        th.style.width = '40px';
        var selectAll = document.createElement('input');
        selectAll.type = 'checkbox';
        selectAll.id = 'images-select-all';
        selectAll.title = 'Select all unused';
        selectAll.addEventListener('change', function() { toggleSelectAll(); });
        th.appendChild(selectAll);
        thead.insertBefore(th, thead.firstChild);
    } else if (!_manageMode && existingTh) {
        existingTh.remove();
    }

    for (var i = 0; i < images.length; i++) {
        var img = images[i];
        var row = document.createElement('tr');

        // Checkbox cell (manage mode only).
        if (_manageMode) {
            var checkCell = document.createElement('td');
            checkCell.className = 'td-checkbox';
            if (!img.in_use) {
                var cb = document.createElement('input');
                cb.type = 'checkbox';
                cb.checked = _selectedIds.has(img.id);
                cb.setAttribute('data-image-id', img.id);
                cb.addEventListener('change', (function(id) {
                    return function() { toggleImageSelect(id); };
                })(img.id));
                checkCell.appendChild(cb);
            }
            row.appendChild(checkCell);
        }

        // Tags cell — each tag on its own line to prevent overlap.
        var tagsCell = document.createElement('td');
        tagsCell.className = 'cell-image-tags';
        if (img.repo_tags && img.repo_tags.length > 0) {
            for (var t = 0; t < img.repo_tags.length; t++) {
                var tagSpan = document.createElement('code');
                tagSpan.className = 'image-tag';
                tagSpan.textContent = img.repo_tags[t];
                tagSpan.title = img.repo_tags[t];
                tagsCell.appendChild(tagSpan);
                if (t < img.repo_tags.length - 1) {
                    tagsCell.appendChild(document.createElement('br'));
                }
            }
        } else {
            var noneSpan = document.createElement('span');
            noneSpan.className = 'text-muted';
            noneSpan.textContent = '<none>';
            tagsCell.appendChild(noneSpan);
        }
        row.appendChild(tagsCell);

        // ID cell
        var idCell = document.createElement('td');
        idCell.className = 'cell-image-id';
        var code = document.createElement('code');
        code.title = img.id;
        code.textContent = img.id.replace('sha256:', '').substring(0, 12);
        idCell.appendChild(code);
        row.appendChild(idCell);

        // Size cell
        var sizeCell = document.createElement('td');
        sizeCell.className = 'cell-image-size';
        sizeCell.textContent = formatBytes(img.size);
        row.appendChild(sizeCell);

        // Created cell
        var createdCell = document.createElement('td');
        createdCell.title = new Date(img.created * 1000).toISOString();
        createdCell.textContent = formatRelativeTime(img.created);
        row.appendChild(createdCell);

        // In Use cell
        var useCell = document.createElement('td');
        var badge = document.createElement('span');
        badge.className = img.in_use ? 'badge badge-success' : 'badge badge-muted';
        badge.textContent = img.in_use ? 'In Use' : 'Unused';
        useCell.appendChild(badge);
        row.appendChild(useCell);

        // Actions cell
        var actionsCell = document.createElement('td');
        if (!_manageMode) {
            var btn = document.createElement('button');
            btn.className = 'btn btn-sm btn-danger';
            btn.textContent = 'Remove';
            if (img.in_use) {
                btn.disabled = true;
                btn.title = 'Cannot remove: image is in use by a container';
            } else {
                btn.setAttribute('data-image-id', img.id);
                btn.addEventListener('click', (function(imageId) {
                    return function() { removeImage(imageId); };
                })(img.id));
            }
            actionsCell.appendChild(btn);
        }
        row.appendChild(actionsCell);

        tbody.appendChild(row);
    }
}

function formatBytes(bytes) {
    if (bytes === 0) return '0 B';
    var k = 1024;
    var sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
    var i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
}

function formatRelativeTime(unixSeconds) {
    var now = Date.now() / 1000;
    var diff = now - unixSeconds;
    if (diff < 60) return 'just now';
    if (diff < 3600) return Math.floor(diff / 60) + 'm ago';
    if (diff < 86400) return Math.floor(diff / 3600) + 'h ago';
    return Math.floor(diff / 86400) + 'd ago';
}

export async function pruneImages() {
    if (!confirm('Remove all dangling (unused, untagged) images?')) return;
    try {
        var resp = await fetch('/api/images/prune', { method: 'POST' });
        if (!resp.ok) throw new Error('HTTP ' + resp.status);
        var data = await resp.json();
        if (window.showToast) {
            window.showToast('Pruned ' + data.images_deleted + ' images, reclaimed ' + formatBytes(data.space_reclaimed));
        }
        loadImages();
    } catch (err) {
        if (window.showToast) window.showToast('Prune failed: ' + err.message, 'error');
    }
}

export async function removeImage(id) {
    if (!confirm('Remove this image? This cannot be undone.')) return;
    try {
        var resp = await fetch('/api/images/' + encodeURIComponent(id), { method: 'DELETE' });
        if (!resp.ok) throw new Error('HTTP ' + resp.status);
        if (window.showToast) window.showToast('Image removed');
        loadImages();
    } catch (err) {
        if (window.showToast) window.showToast('Remove failed: ' + err.message, 'error');
    }
}
