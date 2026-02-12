/* ============================================================
   Docker-Sentinel Auth — Client-side JavaScript
   ES5-compatible (no let/const/arrow functions)
   ============================================================ */

/* ------------------------------------------------------------
   1. CSRF Helper
   ------------------------------------------------------------ */

function getCSRFToken() {
    var cookies = document.cookie.split(";");
    for (var i = 0; i < cookies.length; i++) {
        var cookie = cookies[i].replace(/^\s+/, "");
        if (cookie.indexOf("sentinel_csrf=") === 0) {
            return cookie.substring("sentinel_csrf=".length);
        }
    }
    return "";
}

function authFetch(url, options) {
    if (!options) options = {};
    if (!options.headers) options.headers = {};

    // Add CSRF token for mutating requests.
    var method = (options.method || "GET").toUpperCase();
    if (method !== "GET" && method !== "HEAD" && method !== "OPTIONS") {
        options.headers["X-CSRF-Token"] = getCSRFToken();
    }

    return fetch(url, options).then(function(resp) {
        // Handle 401 — redirect to login.
        if (resp.status === 401) {
            window.location.href = "/login";
            return Promise.reject(new Error("Unauthorized"));
        }
        return resp;
    });
}

/* ------------------------------------------------------------
   2. Change Password
   ------------------------------------------------------------ */

function initChangePassword() {
    var form = document.getElementById("change-password-form");
    if (!form) return;

    form.addEventListener("submit", function(e) {
        e.preventDefault();
        var currentPw = document.getElementById("current-password").value;
        var newPw = document.getElementById("new-password").value;

        authFetch("/api/auth/change-password", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ current_password: currentPw, new_password: newPw })
        })
            .then(function(resp) {
                return resp.json().then(function(data) { return { ok: resp.ok, data: data }; });
            })
            .then(function(result) {
                if (result.ok) {
                    showToast("Password updated", "success");
                    form.reset();
                } else {
                    showToast(result.data.error || "Failed to update password", "error");
                }
            })
            .catch(function(err) {
                if (err.message !== "Unauthorized") {
                    showToast("Network error", "error");
                }
            });
    });
}

/* ------------------------------------------------------------
   3. Session Management
   ------------------------------------------------------------ */

function revokeSession(token) {
    authFetch("/api/auth/sessions/" + encodeURIComponent(token), {
        method: "DELETE"
    })
        .then(function(resp) {
            return resp.json().then(function(data) { return { ok: resp.ok, data: data }; });
        })
        .then(function(result) {
            if (result.ok) {
                showToast("Session revoked", "success");
                window.location.reload();
            } else {
                showToast(result.data.error || "Failed to revoke session", "error");
            }
        })
        .catch(function(err) {
            if (err.message !== "Unauthorized") {
                showToast("Network error", "error");
            }
        });
}

function revokeAllSessions() {
    authFetch("/api/auth/sessions", {
        method: "DELETE"
    })
        .then(function(resp) {
            return resp.json().then(function(data) { return { ok: resp.ok, data: data }; });
        })
        .then(function(result) {
            if (result.ok) {
                showToast("All other sessions revoked", "success");
                window.location.reload();
            } else {
                showToast(result.data.error || "Failed to revoke sessions", "error");
            }
        })
        .catch(function(err) {
            if (err.message !== "Unauthorized") {
                showToast("Network error", "error");
            }
        });
}

/* ------------------------------------------------------------
   4. API Token Management
   ------------------------------------------------------------ */

function createToken() {
    var nameInput = document.getElementById("new-token-name");
    var name = nameInput ? nameInput.value : "";
    if (!name) {
        showToast("Enter a token name", "error");
        return;
    }

    authFetch("/api/auth/tokens", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: name })
    })
        .then(function(resp) {
            return resp.json().then(function(data) { return { ok: resp.ok, data: data }; });
        })
        .then(function(result) {
            if (result.ok) {
                showToast("Token created", "success");
                var display = document.getElementById("new-token-display");
                var value = document.getElementById("new-token-value");
                if (display && value) {
                    display.style.display = "";
                    value.textContent = result.data.token;
                }
                if (nameInput) nameInput.value = "";
                // Reload after a delay to show the new token in the list.
                setTimeout(function() { window.location.reload(); }, 5000);
            } else {
                showToast(result.data.error || "Failed to create token", "error");
            }
        })
        .catch(function(err) {
            if (err.message !== "Unauthorized") {
                showToast("Network error", "error");
            }
        });
}

function deleteToken(id) {
    authFetch("/api/auth/tokens/" + encodeURIComponent(id), {
        method: "DELETE"
    })
        .then(function(resp) {
            return resp.json().then(function(data) { return { ok: resp.ok, data: data }; });
        })
        .then(function(result) {
            if (result.ok) {
                showToast("Token deleted", "success");
                window.location.reload();
            } else {
                showToast(result.data.error || "Failed to delete token", "error");
            }
        })
        .catch(function(err) {
            if (err.message !== "Unauthorized") {
                showToast("Network error", "error");
            }
        });
}

function copyToken() {
    var el = document.getElementById("new-token-value");
    if (!el) return;
    if (navigator.clipboard) {
        navigator.clipboard.writeText(el.textContent).then(function() {
            showToast("Copied to clipboard", "info");
        });
    }
}

/* ------------------------------------------------------------
   5. User Management (settings page, admin only)
   ------------------------------------------------------------ */

function loadUsers() {
    var container = document.getElementById("user-list");
    if (!container) return;

    authFetch("/api/auth/users")
        .then(function(resp) { return resp.json(); })
        .then(function(users) {
            if (!Array.isArray(users)) return;

            // Build rows using safe DOM methods.
            while (container.firstChild) container.removeChild(container.firstChild);

            for (var i = 0; i < users.length; i++) {
                var u = users[i];
                var tr = document.createElement("tr");

                var tdName = document.createElement("td");
                tdName.textContent = u.username;
                tr.appendChild(tdName);

                var tdRole = document.createElement("td");
                var roleBadge = document.createElement("span");
                roleBadge.className = "badge badge-info";
                roleBadge.textContent = u.role_id;
                tdRole.appendChild(roleBadge);
                tr.appendChild(tdRole);

                var tdStatus = document.createElement("td");
                var statusBadge = document.createElement("span");
                if (u.locked) {
                    statusBadge.className = "badge badge-error";
                    statusBadge.textContent = "Locked";
                } else {
                    statusBadge.className = "badge badge-success";
                    statusBadge.textContent = "Active";
                }
                tdStatus.appendChild(statusBadge);
                tr.appendChild(tdStatus);

                var tdActions = document.createElement("td");
                var delBtn = document.createElement("button");
                delBtn.className = "btn btn-error";
                delBtn.textContent = "Delete";
                delBtn.setAttribute("data-user-id", u.id);
                delBtn.addEventListener("click", function() {
                    deleteUser(this.getAttribute("data-user-id"));
                });
                tdActions.appendChild(delBtn);
                tr.appendChild(tdActions);

                container.appendChild(tr);
            }
        })
        .catch(function() {});
}

function createUser() {
    var username = document.getElementById("new-user-username");
    var password = document.getElementById("new-user-password");
    var role = document.getElementById("new-user-role");
    if (!username || !password || !role) return;

    authFetch("/api/auth/users", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
            username: username.value,
            password: password.value,
            role_id: role.value
        })
    })
        .then(function(resp) {
            return resp.json().then(function(data) { return { ok: resp.ok, data: data }; });
        })
        .then(function(result) {
            if (result.ok) {
                showToast("User created", "success");
                username.value = "";
                password.value = "";
                loadUsers();
            } else {
                showToast(result.data.error || "Failed to create user", "error");
            }
        })
        .catch(function(err) {
            if (err.message !== "Unauthorized") {
                showToast("Network error", "error");
            }
        });
}

function deleteUser(id) {
    authFetch("/api/auth/users/" + encodeURIComponent(id), {
        method: "DELETE"
    })
        .then(function(resp) {
            return resp.json().then(function(data) { return { ok: resp.ok, data: data }; });
        })
        .then(function(result) {
            if (result.ok) {
                showToast("User deleted", "success");
                loadUsers();
            } else {
                showToast(result.data.error || "Failed to delete user", "error");
            }
        })
        .catch(function(err) {
            if (err.message !== "Unauthorized") {
                showToast("Network error", "error");
            }
        });
}

function confirmAuthToggle(checkbox) {
    if (!checkbox.checked) {
        var confirmed = window.confirm(
            "Disable authentication?\n\n" +
            "All endpoints will be accessible without login. " +
            "This is intended for trusted LAN usage only."
        );
        if (!confirmed) {
            checkbox.checked = true;
            return;
        }
    }
    toggleAuth(checkbox.checked);
}

function toggleAuth(enabled) {
    authFetch("/api/auth/settings", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ auth_enabled: enabled })
    })
        .then(function(resp) {
            return resp.json().then(function(data) { return { ok: resp.ok, data: data }; });
        })
        .then(function(result) {
            if (result.ok) {
                showToast(enabled ? "Authentication enabled" : "Authentication disabled", "success");
                window.location.reload();
            } else {
                showToast(result.data.error || "Failed to toggle auth", "error");
            }
        })
        .catch(function(err) {
            if (err.message !== "Unauthorized") {
                showToast("Network error", "error");
            }
        });
}

/* ------------------------------------------------------------
   6. Nav User Dropdown
   ------------------------------------------------------------ */

function toggleUserDropdown(e) {
    e.stopPropagation();
    var dropdown = document.getElementById("user-dropdown");
    if (!dropdown) return;
    var btn = e.currentTarget;
    var isOpen = dropdown.classList.contains("open");
    if (isOpen) {
        dropdown.classList.remove("open");
        btn.setAttribute("aria-expanded", "false");
    } else {
        dropdown.classList.add("open");
        btn.setAttribute("aria-expanded", "true");
    }
}

function closeUserDropdown() {
    var dropdown = document.getElementById("user-dropdown");
    if (!dropdown) return;
    dropdown.classList.remove("open");
    var btn = dropdown.parentElement ? dropdown.parentElement.querySelector(".nav-user-btn") : null;
    if (btn) btn.setAttribute("aria-expanded", "false");
}

/* ------------------------------------------------------------
   7. Init
   ------------------------------------------------------------ */

document.addEventListener("DOMContentLoaded", function() {
    initChangePassword();
    loadUsers();

    // Intercept login form to handle suggest_passkey response.
    var loginForm = document.getElementById("login-form");
    if (loginForm) {
        loginForm.addEventListener("submit", function(e) {
            e.preventDefault();
            var username = document.getElementById("username").value;
            var password = document.getElementById("password").value;
            var btn = loginForm.querySelector(".login-btn[type=submit]");
            if (btn) { btn.disabled = true; btn.textContent = "Signing in..."; }

            fetch("/login", {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ username: username, password: password })
            })
            .then(function(resp) {
                return resp.json().then(function(data) { return { ok: resp.ok, status: resp.status, data: data }; });
            })
            .then(function(result) {
                if (result.ok) {
                    if (result.data.suggest_passkey) {
                        sessionStorage.setItem("suggest_passkey", "1");
                    }
                    window.location.href = result.data.redirect || "/";
                } else {
                    var errDiv = document.querySelector(".login-error");
                    if (!errDiv) {
                        errDiv = document.createElement("div");
                        errDiv.className = "login-error";
                        loginForm.parentNode.insertBefore(errDiv, loginForm);
                    }
                    errDiv.textContent = result.data.error || "Invalid username or password";
                    if (btn) { btn.disabled = false; btn.textContent = "Sign In"; }
                }
            })
            .catch(function() {
                var errDiv = document.querySelector(".login-error");
                if (!errDiv) {
                    errDiv = document.createElement("div");
                    errDiv.className = "login-error";
                    loginForm.parentNode.insertBefore(errDiv, loginForm);
                }
                errDiv.textContent = "Network error";
                if (btn) { btn.disabled = false; btn.textContent = "Sign In"; }
            });
        });
    }

    // Close user dropdown when clicking outside.
    document.addEventListener("click", function(e) {
        var navUser = document.querySelector(".nav-user");
        if (navUser && !navUser.contains(e.target)) {
            closeUserDropdown();
        }
    });

    // Close user dropdown on Escape key.
    document.addEventListener("keydown", function(e) {
        if (e.key === "Escape" || e.keyCode === 27) {
            closeUserDropdown();
        }
    });
});
