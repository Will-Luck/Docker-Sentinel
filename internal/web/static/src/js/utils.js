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

// _clickToasts holds, by subject key, the toast a click showed about that
// subject while it is on screen.
var _clickToasts = Object.create(null);

// showToast shows a toast at once. subject (toastSubjectFor) names what a
// click's toast is about: the toast of the events the click causes then
// takes its place (_flushBatchToasts), so the tab that clicked shows one
// toast about it, as every other tab does.
function showToast(message, type, subject) {
    if (!subject) {
        _showToastImmediate(message, type);
        return;
    }
    var key = subject.key;
    var shown = _showToastImmediate(message, type, function () {
        if (_clickToasts[key] === shown) delete _clickToasts[key];
    });
    _clickToasts[key] = shown;
}

// toastSubjectFor names a container or Swarm service on a host (empty for
// this one) as what toasts are about. A service and a container of one name
// on one host are one subject, as they are one entry in the update queue,
// whose events do not say which of the two they are about.
function toastSubjectFor(hostId, name) {
    return {key: (hostId || "") + "::" + name, name: name};
}

// _showToastImmediate shows a toast for 4 s and returns it (for _setToast);
// onFade, when given, runs as it starts to fade.
function _showToastImmediate(message, type, onFade) {
    var container = document.getElementById("toast-container");
    if (!container) {
        container = document.createElement("div");
        container.id = "toast-container";
        container.className = "toast-container";
        document.body.appendChild(container);
    }

    var shown = {el: document.createElement("div"), fade: null, onFade: onFade || null};
    _setToast(shown, message, type);
    container.appendChild(shown.el);
    return shown;
}

// _setToast gives a shown toast its text and type, and 4 s on screen from now.
function _setToast(shown, message, type) {
    var toast = shown.el;
    toast.className = "toast toast-" + type;
    toast.textContent = message;
    if (shown.fade) clearTimeout(shown.fade);
    shown.fade = setTimeout(function () {
        if (shown.onFade) shown.onFade();
        toast.style.animation = "fadeOut 0.3s ease-out forwards";
        setTimeout(function () {
            if (toast.parentNode) toast.parentNode.removeChild(toast);
        }, 300);
    }, 4000);
}

// Queue a toast for batching. Toasts that arrive within the batch window are
// told by what they are about (subject: {key, name}, for example one
// container on one host): those about one subject as one toast that names it,
// several subjects as one summary that counts subjects, not events. One action
// sends several events (an approve: "approved", then "update started"), and
// counting each event as an update told one approve as "2 updates detected".
// A toast with no subject is a subject of its own.
function queueBatchToast(message, type, subject) {
    _toastBatch.push({message: message, type: type, subject: subject || null});
    if (_toastBatchTimer) clearTimeout(_toastBatchTimer);
    _toastBatchTimer = setTimeout(function () {
        _flushBatchToasts();
    }, _toastBatchWindow);
}

// _groupBySubject groups toasts by subject, in the order subjects first came.
function _groupBySubject(batch) {
    var groups = [];
    var byKey = Object.create(null);
    for (var i = 0; i < batch.length; i++) {
        var t = batch[i];
        var g = t.subject ? byKey[t.subject.key] : null;
        if (!g) {
            g = {key: t.subject ? t.subject.key : "", name: t.subject ? t.subject.name : "", toasts: []};
            groups.push(g);
            if (t.subject) byKey[t.subject.key] = g;
        }
        g.toasts.push(t);
    }
    return groups;
}

// _groupText tells one subject's toasts in one line: each message once, in
// the order they came, after the subject's name unless they already name it.
function _groupText(g) {
    var msgs = [];
    for (var i = 0; i < g.toasts.length; i++) {
        if (msgs.indexOf(g.toasts[i].message) === -1) msgs.push(g.toasts[i].message);
    }
    var text = msgs.join(", ");
    if (g.name && !_namesSubject(text, g.name)) text = g.name + ": " + text;
    return text;
}

// _namesSubject reports whether text names name as a whole name, not as
// part of a word or of a longer name ("app" in "approved"). Container and
// service names hold only letters, digits and "_", "." and "-".
function _namesSubject(text, name) {
    var escaped = name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    return new RegExp("(^|[^A-Za-z0-9_.-])" + escaped + "($|[^A-Za-z0-9_.-])").test(text);
}

var _toastSeverity = {info: 0, success: 1, warning: 2, error: 3};

// _groupType is the most severe type among a subject's toasts.
function _groupType(g) {
    var type = g.toasts[0].type;
    for (var i = 1; i < g.toasts.length; i++) {
        if ((_toastSeverity[g.toasts[i].type] || 0) > (_toastSeverity[type] || 0)) type = g.toasts[i].type;
    }
    return type;
}

// _isQueueMessage reports whether a message says a container was queued for
// approval; any other counts as an update.
function _isQueueMessage(message) {
    var msg = message.toLowerCase();
    if (msg.indexOf("update") !== -1 || msg.indexOf("available") !== -1) return false;
    return msg.indexOf("queue") !== -1 || msg.indexOf("added") !== -1;
}

function _flushBatchToasts() {
    var batch = _toastBatch;
    _toastBatch = [];
    _toastBatchTimer = null;
    if (batch.length === 0) return;
    var groups = _groupBySubject(batch);
    if (groups.length === 1) {
        var g = groups[0];
        var clicked = g.key ? _clickToasts[g.key] : null;
        if (clicked) {
            // The events of a click take the place of its toast, once.
            delete _clickToasts[g.key];
            _setToast(clicked, _groupText(g), _groupType(g));
        } else {
            _showToastImmediate(_groupText(g), _groupType(g));
        }
        return;
    }
    // Summarise: count the subjects with an update and those queued.
    var updates = 0;
    var queued = 0;
    for (var i = 0; i < groups.length; i++) {
        var hasUpdate = false;
        var hasQueued = false;
        for (var j = 0; j < groups[i].toasts.length; j++) {
            if (_isQueueMessage(groups[i].toasts[j].message)) hasQueued = true;
            else hasUpdate = true;
        }
        if (hasUpdate) updates++;
        if (hasQueued) queued++;
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
    // Also safe inside a quoted attribute.
    return div.innerHTML.replace(/"/g, "&quot;").replace(/'/g, "&#39;");
}

/* ------------------------------------------------------------
   4a. Styled Confirmation Modal
   ------------------------------------------------------------ */

function showConfirm(title, bodyHTML, opts) {
    // bodyHTML is always application-generated markup (never user input),
    // constructed from escapeHTML-sanitised values in calling code.
    // opts.confirmLabel — custom label for the confirm button (default: "Apply")
    // opts.danger       — when true, use confirm-btn-danger instead of confirm-btn-apply
    if (!opts) opts = {};
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
        applyBtn.className = opts.danger ? "confirm-btn-danger" : "confirm-btn-apply";
        applyBtn.textContent = opts.confirmLabel || "Apply";
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

// apiPost posts body (JSON, when given) to url. Its success toast (the
// server's message, else successMsg) is about subject when one is given
// (showToast); its error toast is never batched.
function apiPost(url, body, successMsg, errorMsg, triggerEl, onSuccess, onError, subject) {
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
                showToast(result.data.message || successMsg, "success", subject);
                if (onSuccess) onSuccess(result.data);
            } else {
                showToast(result.data.error || errorMsg, "error");
                if (onError) onError();
            }
            clearLoading();
        })
        .catch(function () {
            clearLoading();
            showToast("Network error \u2014 " + errorMsg.toLowerCase(), "error");
            if (onError) onError();
        });
}

/* ------------------------------------------------------------
   5a. apiFetch helper (generalised fetch wrapper)
   ------------------------------------------------------------ */

function apiFetch(url, opts) {
    opts = opts || {};
    var method = opts.method || "GET";
    var triggerEl = opts.triggerEl || null;

    if (triggerEl) {
        triggerEl.classList.add("loading");
        triggerEl.disabled = true;
    }

    var fetchOpts = { method: method, headers: {} };
    if (opts.body) {
        fetchOpts.headers["Content-Type"] = "application/json";
        fetchOpts.body = typeof opts.body === "string" ? opts.body : JSON.stringify(opts.body);
    }

    function clearLoading() {
        if (triggerEl) {
            triggerEl.classList.remove("loading");
            triggerEl.disabled = false;
        }
    }

    return fetch(url, fetchOpts)
        .then(function (resp) {
            return resp.json().then(function (data) {
                return { ok: resp.ok, data: data };
            });
        })
        .then(function (result) {
            clearLoading();
            if (result.ok) {
                if (opts.successMsg) showToast(result.data.message || opts.successMsg, "success");
                if (opts.onSuccess) opts.onSuccess(result.data);
            } else {
                var msg = result.data.error || (opts.errorMsg || "Error");
                showToast(msg, "error");
                if (opts.onError) opts.onError(new Error(msg));
            }
            return result.data;
        })
        .catch(function (err) {
            clearLoading();
            var msg = (opts.errorMsg || "Error") + ": " + err.message;
            showToast(msg, "error");
            if (opts.onError) opts.onError(err);
            throw err;
        });
}

export {
    showToast,
    toastSubjectFor,
    queueBatchToast,
    escapeHTML,
    showConfirm,
    apiPost,
    apiFetch
};
