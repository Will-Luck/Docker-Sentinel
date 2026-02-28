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
        // Auto-redirect to login on 401 (session expired).
        if (resp.status === 401 && url.indexOf("/api/auth/me") === -1) {
            window.location.href = "/login";
        }
        return resp;
    });
};

export { getCSRFToken };
