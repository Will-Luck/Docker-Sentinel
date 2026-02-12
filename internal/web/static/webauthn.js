/* ============================================================
   Docker-Sentinel WebAuthn — Passkey Client
   ES5-compatible (no let/const/arrow functions)
   ============================================================ */

/* ------------------------------------------------------------
   0. Inject Prompt Styles
   ------------------------------------------------------------ */

(function() {
    var style = document.createElement("style");
    style.textContent = ".passkey-prompt-overlay{position:fixed;top:0;left:0;right:0;bottom:0;background:rgba(0,0,0,0.5);display:flex;align-items:center;justify-content:center;z-index:1000;padding:var(--sp-4)}" +
        ".passkey-prompt-card{background:var(--bg-surface);border:1px solid var(--border);border-radius:var(--radius-lg);padding:var(--sp-6);max-width:420px;width:100%;box-shadow:var(--shadow-lg)}";
    document.head.appendChild(style);
})();

/* ------------------------------------------------------------
   1. Base64URL Helpers
   ------------------------------------------------------------ */

function base64urlEncode(buffer) {
    var bytes = new Uint8Array(buffer);
    var binary = "";
    for (var i = 0; i < bytes.length; i++) {
        binary += String.fromCharCode(bytes[i]);
    }
    return btoa(binary)
        .replace(/\+/g, "-")
        .replace(/\//g, "_")
        .replace(/=+$/, "");
}

function base64urlDecode(str) {
    str = str.replace(/-/g, "+").replace(/_/g, "/");
    while (str.length % 4 !== 0) {
        str += "=";
    }
    var binary = atob(str);
    var bytes = new Uint8Array(binary.length);
    for (var i = 0; i < binary.length; i++) {
        bytes[i] = binary.charCodeAt(i);
    }
    return bytes.buffer;
}

/* ------------------------------------------------------------
   2. Registration (Account page)
   ------------------------------------------------------------ */

function registerPasskey() {
    if (!window.PublicKeyCredential || !navigator.credentials) {
        showToast("Passkeys require HTTPS. Access Sentinel via its HTTPS proxy URL.", "error");
        return;
    }

    var nameInput = document.getElementById("passkey-name");
    var name = nameInput ? nameInput.value.trim() : "";
    if (!name) name = "Passkey";

    var btn = document.getElementById("register-passkey-btn");
    if (btn) {
        btn.disabled = true;
        btn.textContent = "Registering...";
    }

    authFetch("/api/auth/passkeys/register/begin", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({})
    })
    .then(function(resp) {
        if (!resp.ok) {
            return resp.json().then(function(data) {
                throw new Error(data.error || "Failed to start registration");
            });
        }
        return resp.json();
    })
    .then(function(options) {
        options.publicKey.challenge = base64urlDecode(options.publicKey.challenge);
        options.publicKey.user.id = base64urlDecode(options.publicKey.user.id);

        if (options.publicKey.excludeCredentials) {
            for (var i = 0; i < options.publicKey.excludeCredentials.length; i++) {
                options.publicKey.excludeCredentials[i].id = base64urlDecode(options.publicKey.excludeCredentials[i].id);
            }
        }

        return navigator.credentials.create(options);
    })
    .then(function(credential) {
        var body = {
            id: credential.id,
            rawId: base64urlEncode(credential.rawId),
            type: credential.type,
            response: {
                attestationObject: base64urlEncode(credential.response.attestationObject),
                clientDataJSON: base64urlEncode(credential.response.clientDataJSON)
            }
        };

        if (credential.response.getTransports) {
            body.response.transports = credential.response.getTransports();
        }

        return authFetch("/api/auth/passkeys/register/finish?name=" + encodeURIComponent(name), {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(body)
        });
    })
    .then(function(resp) {
        return resp.json().then(function(data) { return { ok: resp.ok, data: data }; });
    })
    .then(function(result) {
        if (result.ok) {
            showToast("Passkey registered successfully", "success");
            if (nameInput) nameInput.value = "";
            setTimeout(function() { window.location.reload(); }, 1000);
        } else {
            showToast(result.data.error || "Failed to register passkey", "error");
        }
    })
    .catch(function(err) {
        if (err.name === "NotAllowedError") {
            showToast("Registration cancelled", "info");
        } else if (err.message !== "Unauthorized") {
            showToast(err.message || "Registration failed", "error");
        }
    })
    .then(function() {
        if (btn) {
            btn.disabled = false;
            btn.textContent = "Register Passkey";
        }
    });
}

/* ------------------------------------------------------------
   3. Login with Passkey (Login page)
   ------------------------------------------------------------ */

function loginWithPasskey() {
    if (!window.PublicKeyCredential || !navigator.credentials) {
        showToast("Passkeys require HTTPS. Access Sentinel via its HTTPS proxy URL.", "error");
        return;
    }

    var btn = document.getElementById("passkey-login-btn");
    if (btn) {
        btn.disabled = true;
        btn.textContent = "Waiting for passkey...";
    }

    fetch("/api/auth/passkeys/login/begin", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({})
    })
    .then(function(resp) {
        if (!resp.ok) {
            return resp.json().then(function(data) {
                throw new Error(data.error || "Failed to start login");
            });
        }
        return resp.json();
    })
    .then(function(options) {
        options.publicKey.challenge = base64urlDecode(options.publicKey.challenge);

        if (options.publicKey.allowCredentials) {
            for (var i = 0; i < options.publicKey.allowCredentials.length; i++) {
                options.publicKey.allowCredentials[i].id = base64urlDecode(options.publicKey.allowCredentials[i].id);
            }
        }

        return navigator.credentials.get(options);
    })
    .then(function(assertion) {
        var body = {
            id: assertion.id,
            rawId: base64urlEncode(assertion.rawId),
            type: assertion.type,
            response: {
                authenticatorData: base64urlEncode(assertion.response.authenticatorData),
                clientDataJSON: base64urlEncode(assertion.response.clientDataJSON),
                signature: base64urlEncode(assertion.response.signature)
            }
        };

        if (assertion.response.userHandle) {
            body.response.userHandle = base64urlEncode(assertion.response.userHandle);
        }

        return fetch("/api/auth/passkeys/login/finish", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(body)
        });
    })
    .then(function(resp) {
        return resp.json().then(function(data) { return { ok: resp.ok, data: data }; });
    })
    .then(function(result) {
        if (result.ok) {
            window.location.href = result.data.redirect || "/";
        } else {
            showToast(result.data.error || "Passkey login failed", "error");
        }
    })
    .catch(function(err) {
        if (err.name === "NotAllowedError") {
            showToast("Login cancelled", "info");
        } else {
            showToast(err.message || "Passkey login failed", "error");
        }
    })
    .then(function() {
        if (btn) {
            btn.disabled = false;
            btn.textContent = "Sign in with passkey";
        }
    });
}

/* ------------------------------------------------------------
   4. Delete Passkey (Account page)
   ------------------------------------------------------------ */

function deletePasskey(credID) {
    if (!window.confirm("Delete this passkey? You cannot undo this.")) return;

    authFetch("/api/auth/passkeys/" + encodeURIComponent(credID), {
        method: "DELETE"
    })
    .then(function(resp) {
        return resp.json().then(function(data) { return { ok: resp.ok, data: data }; });
    })
    .then(function(result) {
        if (result.ok) {
            showToast("Passkey deleted", "success");
            window.location.reload();
        } else {
            showToast(result.data.error || "Failed to delete passkey", "error");
        }
    })
    .catch(function(err) {
        if (err.message !== "Unauthorized") {
            showToast("Network error", "error");
        }
    });
}

/* ------------------------------------------------------------
   5. Load Passkeys (Account page)
   ------------------------------------------------------------ */

function loadPasskeys() {
    var container = document.getElementById("passkey-list");
    if (!container) return;

    authFetch("/api/auth/passkeys")
        .then(function(resp) { return resp.json(); })
        .then(function(passkeys) {
            while (container.firstChild) container.removeChild(container.firstChild);

            if (!Array.isArray(passkeys) || passkeys.length === 0) {
                var tr = document.createElement("tr");
                var td = document.createElement("td");
                td.setAttribute("colspan", "3");
                td.style.textAlign = "center";
                td.style.color = "var(--fg-secondary)";
                td.textContent = "No passkeys registered";
                tr.appendChild(td);
                container.appendChild(tr);
                return;
            }

            for (var i = 0; i < passkeys.length; i++) {
                var pk = passkeys[i];
                var tr = document.createElement("tr");

                var tdName = document.createElement("td");
                tdName.textContent = pk.name;
                tr.appendChild(tdName);

                var tdCreated = document.createElement("td");
                tdCreated.textContent = pk.created_at ? new Date(pk.created_at).toLocaleDateString() : "-";
                tr.appendChild(tdCreated);

                var tdActions = document.createElement("td");
                var delBtn = document.createElement("button");
                delBtn.className = "btn btn-error";
                delBtn.textContent = "Delete";
                delBtn.setAttribute("data-passkey-id", pk.id);
                delBtn.addEventListener("click", function() {
                    deletePasskey(this.getAttribute("data-passkey-id"));
                });
                tdActions.appendChild(delBtn);
                tr.appendChild(tdActions);

                container.appendChild(tr);
            }
        })
        .catch(function() {});
}

/* ------------------------------------------------------------
   6. Post-Login Passkey Prompt
   ------------------------------------------------------------ */

function initPasskeyPrompt() {
    if (window.location.pathname !== "/") return;

    if (sessionStorage.getItem("suggest_passkey") !== "1") return;
    sessionStorage.removeItem("suggest_passkey");

    var dismissed = localStorage.getItem("passkey_prompt_dismissed");
    if (dismissed) {
        var dismissedAt = parseInt(dismissed, 10);
        if (Date.now() - dismissedAt < 30 * 24 * 60 * 60 * 1000) return;
    }

    if (!window.PublicKeyCredential) return;

    var overlay = document.createElement("div");
    overlay.className = "passkey-prompt-overlay";

    var card = document.createElement("div");
    card.className = "passkey-prompt-card";

    var heading = document.createElement("h3");
    heading.style.cssText = "margin:0 0 var(--sp-3) 0;font-size:1.1rem";
    heading.textContent = "Secure your account with a passkey";
    card.appendChild(heading);

    var desc = document.createElement("p");
    desc.style.cssText = "margin:0 0 var(--sp-4) 0;font-size:0.9rem;color:var(--fg-secondary)";
    desc.textContent = "Passkeys use your fingerprint, face, or screen lock for fast, phishing-resistant sign-in.";
    card.appendChild(desc);

    var btnWrap = document.createElement("div");
    btnWrap.style.cssText = "display:flex;gap:var(--sp-3);justify-content:flex-end";

    var laterBtn = document.createElement("button");
    laterBtn.className = "btn";
    laterBtn.textContent = "Maybe later";
    laterBtn.addEventListener("click", function() {
        localStorage.setItem("passkey_prompt_dismissed", String(Date.now()));
        overlay.parentNode.removeChild(overlay);
    });
    btnWrap.appendChild(laterBtn);

    var setupBtn = document.createElement("button");
    setupBtn.className = "btn btn-success";
    setupBtn.textContent = "Set up now";
    setupBtn.addEventListener("click", function() {
        window.location.href = "/account#passkeys";
    });
    btnWrap.appendChild(setupBtn);

    card.appendChild(btnWrap);
    overlay.appendChild(card);
    document.body.appendChild(overlay);
}

/* ------------------------------------------------------------
   7. Init
   ------------------------------------------------------------ */

document.addEventListener("DOMContentLoaded", function() {
    loadPasskeys();
    initPasskeyPrompt();
});
