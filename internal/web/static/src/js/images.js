/* ============================================================
   Docker-Sentinel — Images management page
   ============================================================ */

export async function loadImages() {
    try {
        var resp = await fetch('/api/images');
        if (!resp.ok) throw new Error('HTTP ' + resp.status);
        var data = await resp.json();
        renderImagesTable(data.images || []);
    } catch (err) {
        console.error('Failed to load images:', err);
    }
}

function renderImagesTable(images) {
    var tbody = document.getElementById('images-tbody');
    if (!tbody) return;

    // Clear existing rows.
    while (tbody.firstChild) tbody.removeChild(tbody.firstChild);

    if (images.length === 0) {
        var emptyRow = document.createElement('tr');
        var emptyCell = document.createElement('td');
        emptyCell.colSpan = 6;
        emptyCell.style.textAlign = 'center';
        emptyCell.style.padding = '2rem';
        emptyCell.style.color = 'var(--text-secondary)';
        emptyCell.textContent = 'No images found';
        emptyRow.appendChild(emptyCell);
        tbody.appendChild(emptyRow);
        return;
    }

    // Sort by created date (newest first).
    images.sort(function(a, b) { return b.created - a.created; });

    // Update summary.
    var totalSize = images.reduce(function(sum, img) { return sum + img.size; }, 0);
    var summaryEl = document.getElementById('images-summary');
    if (summaryEl) {
        summaryEl.textContent = images.length + ' images, ' + formatBytes(totalSize) + ' total';
    }

    for (var i = 0; i < images.length; i++) {
        var img = images[i];
        var row = document.createElement('tr');

        // Tags cell
        var tagsCell = document.createElement('td');
        if (img.repo_tags && img.repo_tags.length > 0) {
            tagsCell.textContent = img.repo_tags.join(', ');
        } else {
            var noneSpan = document.createElement('span');
            noneSpan.className = 'text-muted';
            noneSpan.textContent = '<none>';
            tagsCell.appendChild(noneSpan);
        }
        row.appendChild(tagsCell);

        // ID cell
        var idCell = document.createElement('td');
        var code = document.createElement('code');
        code.title = img.id;
        code.textContent = img.id.replace('sha256:', '').substring(0, 12);
        idCell.appendChild(code);
        row.appendChild(idCell);

        // Size cell
        var sizeCell = document.createElement('td');
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
