/* ============================================================
   0. CSRF Protection — auto-inject X-CSRF-Token on mutating requests
   ============================================================ */

var originalFetch = window.fetch;

function getCSRFToken() {
    var match = document.cookie.match(/(^|;\s*)sentinel_csrf=([^;]+)/);
    return match ? match[2] : "";
}

window.fetch = function (url, opts) {
    opts = opts || {};
    var method = (opts.method || "GET").toUpperCase();
    // Only inject CSRF on state-changing methods for same-origin requests.
    if (method !== "GET" && method !== "HEAD" && method !== "OPTIONS") {
        var token = getCSRFToken();
        if (token) {
            opts.headers = opts.headers || {};
            // Support both Headers object and plain object.
            if (typeof opts.headers.set === "function") {
                opts.headers.set("X-CSRF-Token", token);
            } else {
                opts.headers["X-CSRF-Token"] = token;
            }
        }
    }
    return originalFetch.call(window, url, opts).then(function (resp) {
        // Redirect to login only when the session is gone. The server also
        // answers 401 for a wrong password or code, and the page must show
        // that error instead of reloading.
        if (resp.status === 401 && String(url).indexOf("/api/auth/me") === -1) {
            return isSessionGone(resp).then(function (gone) {
                if (gone) {
                    window.location.href = "/login";
                }
                return resp;
            });
        }
        return resp;
    });
};

// isSessionGone resolves true when a 401 response means "not signed in".
function isSessionGone(resp) {
    return resp.clone().json().then(function (body) {
        return !!body && body.error === "authentication required";
    }, function () {
        return false;
    });
}

export { getCSRFToken, isSessionGone };
