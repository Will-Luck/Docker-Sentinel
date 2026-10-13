/* ============================================================
   Selection keys: a container is identified by host and name
   ============================================================ */

// A hub container and an agent container can share a name, so the dashboard
// keys a selected row by host and name. A local row keeps its bare name,
// which matches the policy key convention (host::name for remote rows).

function selectionKey(host, name) {
    return host ? host + "::" + name : name;
}

function parseSelectionKey(key) {
    var i = key.lastIndexOf("::");
    if (i === -1) return { host: "", name: key };
    return { host: key.slice(0, i), name: key.slice(i + 2) };
}

// containerActionURL is the endpoint for a lifecycle action on a selected
// row. Remote rows send ?host=, as the container page does.
function containerActionURL(key, action) {
    var t = parseSelectionKey(key);
    var url = "/api/containers/" + encodeURIComponent(t.name) + "/" + action;
    if (t.host) url += "?host=" + encodeURIComponent(t.host);
    return url;
}

// Host names seen on ticked rows, so dialogs can say "web (Agent One)"
// instead of a 32-character host ID.
var hostNames = {};

function shortHostID(host) {
    return host.length > 12 ? host.slice(0, 8) : host;
}

// selectionLabel names a selected row for dialogs and toasts.
function selectionLabel(key) {
    var t = parseSelectionKey(key);
    if (!t.host) return t.name;
    return t.name + " (" + (hostNames[t.host] || shortHostID(t.host)) + ")";
}

// policyTargets turns selection keys into {name, host} pairs for the bulk
// policy API.
function policyTargets(keys) {
    return keys.map(function (k) { return parseSelectionKey(k); }).map(function (t) {
        return { name: t.name, host: t.host };
    });
}

// checkboxKey is the selection key of a dashboard row checkbox.
function checkboxKey(cb) {
    var host = cb.getAttribute("data-host") || "";
    var hostName = cb.getAttribute("data-host-name");
    if (host && hostName) hostNames[host] = hostName;
    return selectionKey(host, cb.value);
}

export { selectionKey, parseSelectionKey, containerActionURL, selectionLabel, policyTargets, checkboxKey };
