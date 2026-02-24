/* ============================================================
   3. Toast System (with batching for scan bursts)
   4. HTML Escape Helper
   4a. Styled Confirmation Modal
   5. apiPost helper
   ============================================================ */

/* ------------------------------------------------------------
   3. Toast System
   ------------------------------------------------------------ */

var _toastBatch = [];
var _toastBatchTimer = null;
var _toastBatchWindow = 1500; // ms to collect events before showing summary

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

    setTimeout(function () {
        toast.style.animation = "fadeOut 0.3s ease-out forwards";
        setTimeout(function () {
            if (toast.parentNode) toast.parentNode.removeChild(toast);
        }, 300);
    }, 4000);
}

// Queue a toast for batching. If multiple arrive within the batch window,
// they are collapsed into a single summary toast.
function queueBatchToast(message, type) {
    _toastBatch.push({message: message, type: type});
    if (_toastBatchTimer) clearTimeout(_toastBatchTimer);
    _toastBatchTimer = setTimeout(function () {
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
    // Summarise: count updates and queue additions separately.
    var updates = 0;
    var queued = 0;
    for (var i = 0; i < batch.length; i++) {
        var msg = batch[i].message.toLowerCase();
        if (msg.indexOf("update") !== -1 || msg.indexOf("available") !== -1) updates++;
        else if (msg.indexOf("queue") !== -1 || msg.indexOf("added") !== -1) queued++;
        else updates++; // fallback
    }
    var parts = [];
    if (updates > 0) parts.push(updates + " update" + (updates === 1 ? "" : "s") + " detected");
    if (queued > 0) parts.push(queued + " queued for approval");
    _showToastImmediate(parts.join(", "), "info");
}

/* ------------------------------------------------------------
   4. HTML Escape Helper
   ------------------------------------------------------------ */

function escapeHTML(str) {
    var div = document.createElement("div");
    div.appendChild(document.createTextNode(str));
    return div.innerHTML;
}

/* ------------------------------------------------------------
   4a. Styled Confirmation Modal
   ------------------------------------------------------------ */

function showConfirm(title, bodyHTML) {
    // bodyHTML is always application-generated markup (never user input),
    // constructed from escapeHTML-sanitised values in calling code.
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
        // Safe: bodyHTML is built from escapeHTML-sanitised values, not raw user input.
        body.innerHTML = bodyHTML; // eslint-disable-line no-unsanitized/property
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

        cancelBtn.addEventListener("click", function() { cleanup(false); });
        applyBtn.addEventListener("click", function() { cleanup(true); });

        overlay.addEventListener("click", function(e) {
            if (e.target === overlay) cleanup(false);
        });

        // Focus trap and Escape
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
                    if (idx <= 0) { e.preventDefault(); focusables[focusables.length - 1].focus(); }
                } else {
                    if (idx >= focusables.length - 1) { e.preventDefault(); focusables[0].focus(); }
                }
            }
        });
    });
}

/* ------------------------------------------------------------
   5. apiPost helper (shared fetch wrapper)
   ------------------------------------------------------------ */

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
    fetch(url, opts)
        .then(function (resp) {
            return resp.json().then(function (data) {
                return { ok: resp.ok, data: data };
            });
        })
        .then(function (result) {
            if (result.ok) {
                showToast(result.data.message || successMsg, "success");
                if (onSuccess) onSuccess(result.data);
            } else {
                showToast(result.data.error || errorMsg, "error");
            }
            clearLoading();
        })
        .catch(function () {
            clearLoading();
            showToast("Network error \u2014 " + errorMsg.toLowerCase(), "error");
        });
}

export {
    showToast,
    queueBatchToast,
    escapeHTML,
    showConfirm,
    apiPost
};
