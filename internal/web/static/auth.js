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
                // Add the new token to the table immediately.
                addTokenRow(result.data);
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
    } else {
        var t = document.createElement("textarea");
        t.value = el.textContent;
        document.body.appendChild(t);
        t.select();
        document.execCommand("copy");
        document.body.removeChild(t);
        showToast("Copied to clipboard", "info");
    }
}

function addTokenRow(data) {
    var tbody = document.getElementById("token-list");
    if (!tbody) return;
    // Remove "No API tokens" placeholder if present.
    var placeholder = tbody.querySelector("td[colspan]");
    if (placeholder) placeholder.closest("tr").remove();

    var tr = document.createElement("tr");
    var cells = [data.name || "", "Just now", "Never", "Never"];
    for (var i = 0; i < cells.length; i++) {
        var td = document.createElement("td");
        td.textContent = cells[i];
        tr.appendChild(td);
    }
    var actionTd = document.createElement("td");
    var btn = document.createElement("button");
    btn.className = "btn btn-error";
    btn.textContent = "Delete";
    btn.onclick = function() { deleteToken(data.id || ""); };
    actionTd.appendChild(btn);
    tr.appendChild(actionTd);
    tbody.appendChild(tr);
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
            "User management, OIDC, and account settings will be disabled " +
            "until authentication is re-enabled.\n\n" +
            "You can re-enable from the Security tab at any time."
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
   7. TOTP Two-Factor Authentication
   ------------------------------------------------------------ */

// showTOTPStep hides the login form and shows the TOTP input.
function showTOTPStep() {
    var loginForm = document.getElementById("login-form");
    var passkeySection = document.getElementById("passkey-login-section");
    var totpStep = document.getElementById("totp-step");
    var loginError = document.querySelector(".login-error");

    if (loginForm) loginForm.style.display = "none";
    if (passkeySection) passkeySection.style.display = "none";
    if (loginError) loginError.style.display = "none";
    if (totpStep) {
        totpStep.style.display = "";
        var input = document.getElementById("totp-code");
        if (input) { input.value = ""; input.focus(); }
    }
}

// backToLogin returns to the login form from the TOTP step.
function backToLogin() {
    var loginForm = document.getElementById("login-form");
    var passkeySection = document.getElementById("passkey-login-section");
    var totpStep = document.getElementById("totp-step");
    var btn = loginForm ? loginForm.querySelector(".login-btn[type=submit]") : null;

    if (totpStep) totpStep.style.display = "none";
    if (loginForm) { loginForm.style.display = ""; }
    if (passkeySection) passkeySection.style.display = "";
    if (btn) { btn.disabled = false; btn.textContent = "Sign In"; }
    window._pendingTOTPToken = null;
}

// submitTOTP sends the TOTP code for verification.
function submitTOTP() {
    var codeInput = document.getElementById("totp-code");
    var errDiv = document.getElementById("totp-error");
    var btn = document.getElementById("totp-submit");
    var code = codeInput ? codeInput.value.trim() : "";

    if (!code) {
        if (errDiv) { errDiv.textContent = "Enter a code"; errDiv.style.display = ""; }
        return;
    }

    if (btn) { btn.disabled = true; btn.textContent = "Verifying..."; }
    if (errDiv) errDiv.style.display = "none";

    fetch("/api/auth/totp/verify", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
            totp_token: window._pendingTOTPToken || "",
            code: code
        })
    })
    .then(function(resp) {
        return resp.json().then(function(data) { return { ok: resp.ok, data: data }; });
    })
    .then(function(result) {
        if (result.ok) {
            window.location.href = result.data.redirect || "/";
        } else {
            if (errDiv) {
                errDiv.textContent = result.data.error || "Verification failed";
                errDiv.style.display = "";
            }
            if (btn) { btn.disabled = false; btn.textContent = "Verify"; }
            if (codeInput) { codeInput.value = ""; codeInput.focus(); }
        }
    })
    .catch(function() {
        if (errDiv) { errDiv.textContent = "Network error"; errDiv.style.display = ""; }
        if (btn) { btn.disabled = false; btn.textContent = "Verify"; }
    });
}

/* ------------------------------------------------------------
   8. TOTP Account Management (2FA setup/disable on account page)
   ------------------------------------------------------------ */

function loadTOTPStatus() {
    var section = document.getElementById("totp-section");
    if (!section) return;

    authFetch("/api/auth/totp/status")
        .then(function(resp) { return resp.json(); })
        .then(function(data) {
            var enabledDiv = document.getElementById("totp-enabled-content");
            var disabledDiv = document.getElementById("totp-disabled-content");
            var codesLeft = document.getElementById("totp-codes-left");

            if (data.totp_enabled) {
                if (enabledDiv) enabledDiv.style.display = "";
                if (disabledDiv) disabledDiv.style.display = "none";
                if (codesLeft) codesLeft.textContent = data.recovery_codes_left + " remaining";
            } else {
                if (enabledDiv) enabledDiv.style.display = "none";
                if (disabledDiv) disabledDiv.style.display = "";
            }
        })
        .catch(function() {});
}

function startTOTPSetup() {
    var setupDiv = document.getElementById("totp-setup-flow");
    var setupBtn = document.getElementById("totp-setup-btn");
    if (setupBtn) setupBtn.disabled = true;

    authFetch("/api/auth/totp/setup", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: "{}"
    })
    .then(function(resp) {
        return resp.json().then(function(data) { return { ok: resp.ok, data: data }; });
    })
    .then(function(result) {
        if (!result.ok) {
            showToast(result.data.error || "Failed to start 2FA setup", "error");
            if (setupBtn) setupBtn.disabled = false;
            return;
        }

        if (setupDiv) {
            setupDiv.style.display = "";
            // Show secret for manual entry.
            var secretEl = document.getElementById("totp-secret-display");
            if (secretEl) secretEl.textContent = result.data.secret;

            // Render QR code using safe DOM methods.
            var qrContainer = document.getElementById("totp-qr");
            if (qrContainer) {
                while (qrContainer.firstChild) qrContainer.removeChild(qrContainer.firstChild);

                if (typeof QRCode !== "undefined") {
                    new QRCode(qrContainer, {
                        text: result.data.qr_url,
                        width: 200,
                        height: 200,
                        colorDark: "#000000",
                        colorLight: "#ffffff"
                    });
                } else {
                    var p = document.createElement("p");
                    p.style.cssText = "font-size:0.8rem;color:var(--fg-secondary);word-break:break-all";
                    p.textContent = "Provisioning URL: " + result.data.qr_url;
                    qrContainer.appendChild(p);
                }
            }
        }
    })
    .catch(function() {
        showToast("Network error", "error");
        if (setupBtn) setupBtn.disabled = false;
    });
}

function confirmTOTPSetup() {
    var codeInput = document.getElementById("totp-confirm-code");
    var code = codeInput ? codeInput.value.trim() : "";
    if (!code) {
        showToast("Enter the 6-digit code from your authenticator app", "error");
        return;
    }

    authFetch("/api/auth/totp/confirm", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ code: code })
    })
    .then(function(resp) {
        return resp.json().then(function(data) { return { ok: resp.ok, data: data }; });
    })
    .then(function(result) {
        if (!result.ok) {
            showToast(result.data.error || "Invalid code", "error");
            return;
        }

        showToast("Two-factor authentication enabled", "success");

        // Show recovery codes.
        var setupDiv = document.getElementById("totp-setup-flow");
        var recoveryDiv = document.getElementById("totp-recovery-display");
        if (setupDiv) setupDiv.style.display = "none";
        if (recoveryDiv) {
            recoveryDiv.style.display = "";
            var codesList = document.getElementById("totp-recovery-codes");
            if (codesList && result.data.recovery_codes) {
                codesList.textContent = result.data.recovery_codes.join("\n");
            }
        }

        // Reload status after a short delay.
        setTimeout(loadTOTPStatus, 500);
    })
    .catch(function() { showToast("Network error", "error"); });
}

function disableTOTP() {
    var pwInput = document.getElementById("totp-disable-password");
    var password = pwInput ? pwInput.value : "";
    if (!password) {
        showToast("Enter your password to disable 2FA", "error");
        return;
    }

    authFetch("/api/auth/totp/disable", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ password: password })
    })
    .then(function(resp) {
        return resp.json().then(function(data) { return { ok: resp.ok, data: data }; });
    })
    .then(function(result) {
        if (result.ok) {
            showToast("Two-factor authentication disabled", "success");
            if (pwInput) pwInput.value = "";
            loadTOTPStatus();
            var recoveryDiv = document.getElementById("totp-recovery-display");
            if (recoveryDiv) recoveryDiv.style.display = "none";
        } else {
            showToast(result.data.error || "Failed to disable 2FA", "error");
        }
    })
    .catch(function() { showToast("Network error", "error"); });
}

function copyRecoveryCodes() {
    var el = document.getElementById("totp-recovery-codes");
    if (!el) return;
    if (navigator.clipboard) {
        navigator.clipboard.writeText(el.textContent).then(function() {
            showToast("Recovery codes copied to clipboard", "info");
        });
    } else {
        var t = document.createElement("textarea");
        t.value = el.textContent;
        document.body.appendChild(t);
        t.select();
        document.execCommand("copy");
        document.body.removeChild(t);
        showToast("Recovery codes copied to clipboard", "info");
    }
}

/* ------------------------------------------------------------
   9. OIDC / SSO Settings
   ------------------------------------------------------------ */

function loadOIDCSettings() {
    var toggle = document.getElementById("oidc-enabled");
    if (!toggle) return; // Not on settings page or not admin.

    authFetch("/api/settings/oidc")
        .then(function(resp) { return resp.json(); })
        .then(function(data) {
            toggle.checked = !!data.enabled;
            updateToggleTextLocal("oidc-enabled-text", !!data.enabled);

            var issuer = document.getElementById("oidc-issuer-url");
            var clientId = document.getElementById("oidc-client-id");
            var clientSecret = document.getElementById("oidc-client-secret");
            var redirectUrl = document.getElementById("oidc-redirect-url");
            var autoCreate = document.getElementById("oidc-auto-create");
            var defaultRole = document.getElementById("oidc-default-role");

            if (issuer) issuer.value = data.issuer_url || "";
            if (clientId) clientId.value = data.client_id || "";
            if (clientSecret) clientSecret.value = data.client_secret || "";
            if (redirectUrl) redirectUrl.value = data.redirect_url || "";
            if (autoCreate) {
                autoCreate.checked = !!data.auto_create;
                updateToggleTextLocal("oidc-auto-create-text", !!data.auto_create);
            }
            if (defaultRole && data.default_role) {
                for (var i = 0; i < defaultRole.options.length; i++) {
                    if (defaultRole.options[i].value === data.default_role) {
                        defaultRole.selectedIndex = i;
                        break;
                    }
                }
            }
        })
        .catch(function() { /* ignore -- settings not available */ });
}

function saveOIDCSettings() {
    var enabled = (document.getElementById("oidc-enabled") || {}).checked || false;
    var issuerUrl = (document.getElementById("oidc-issuer-url") || {}).value || "";
    var clientId = (document.getElementById("oidc-client-id") || {}).value || "";
    var clientSecret = (document.getElementById("oidc-client-secret") || {}).value || "";
    var redirectUrl = (document.getElementById("oidc-redirect-url") || {}).value || "";
    var autoCreate = (document.getElementById("oidc-auto-create") || {}).checked || false;
    var defaultRole = (document.getElementById("oidc-default-role") || {}).value || "viewer";

    // Auto-detect redirect URL if empty and enabled.
    if (enabled && !redirectUrl) {
        redirectUrl = window.location.origin + "/api/auth/oidc/callback";
    }

    authFetch("/api/settings/oidc", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
            enabled: enabled,
            issuer_url: issuerUrl,
            client_id: clientId,
            client_secret: clientSecret,
            redirect_url: redirectUrl,
            auto_create: autoCreate,
            default_role: defaultRole
        })
    })
    .then(function(resp) {
        return resp.json().then(function(data) { return { ok: resp.ok, data: data }; });
    })
    .then(function(result) {
        if (result.ok) {
            if (result.data.warning) {
                showToast(result.data.warning, "warning");
            } else {
                showToast("OIDC settings saved", "success");
            }
        } else {
            showToast(result.data.error || "Failed to save OIDC settings", "error");
        }
    })
    .catch(function(err) {
        if (err.message !== "Unauthorized") {
            showToast("Network error", "error");
        }
    });
}

// Local helper — avoids dependency on app.js updateToggleText which may not be loaded.
function updateToggleTextLocal(textId, enabled) {
    var text = document.getElementById(textId);
    if (text) {
        text.textContent = enabled ? "On" : "Off";
    }
}

/* ------------------------------------------------------------
   10. Init
   ------------------------------------------------------------ */

document.addEventListener("DOMContentLoaded", function() {
    initChangePassword();
    loadUsers();
    loadTOTPStatus();
    loadOIDCSettings();

    // Intercept login form to handle suggest_passkey and TOTP responses.
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
                    // Check if TOTP is required.
                    if (result.data.totp_required) {
                        window._pendingTOTPToken = result.data.totp_token;
                        showTOTPStep();
                        return;
                    }
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

    // Submit TOTP on Enter key.
    var totpInput = document.getElementById("totp-code");
    if (totpInput) {
        totpInput.addEventListener("keydown", function(e) {
            if (e.key === "Enter" || e.keyCode === 13) {
                e.preventDefault();
                submitTOTP();
            }
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
