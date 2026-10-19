/* Fleetwatch status widget: a status pill (and a banner while a notice is open) for your
 * own website, and the same news in your Crisp or Intercom chat.
 *   <script src="https://<dashboard>/api/public/widget.js" data-page="slug" async></script>
 * Options (data-*): page, position (bottom-right | bottom-left), theme (auto | light | dark),
 * mode (both | pill | banner), chat (auto | crisp | intercom | none).
 */
(function () {
  var me = document.currentScript;
  if (!me) return;
  var opt = function (k, d) { return me.getAttribute("data-" + k) || d; };
  var base = new URL(me.src).origin;
  var page = opt("page", "");
  var pos = opt("position", "bottom-right");
  var mode = opt("mode", "both");
  var chat = opt("chat", "auto");
  var theme = opt("theme", "auto");
  var api = base + "/api/public/status" + (page ? "/" + encodeURIComponent(page) : "");
  var link = base + "/status" + (page ? "/" + encodeURIComponent(page) : "");
  var LABEL = { operational: "All systems operational", degraded: "Some systems are slow", partial: "Partial outage", major: "Major outage", maintenance: "Under maintenance" };
  var COLOR = { operational: "#16a34a", degraded: "#d97706", partial: "#ea580c", major: "#dc2626", maintenance: "#2563eb" };
  var dark = theme === "dark" || (theme === "auto" && window.matchMedia && matchMedia("(prefers-color-scheme: dark)").matches);
  var bg = dark ? "#18181b" : "#ffffff", fg = dark ? "#f4f4f5" : "#18181b", muted = dark ? "#a1a1aa" : "#71717a", line = dark ? "#27272a" : "#e4e4e7";
  var store = { get: function (k) { try { return localStorage.getItem(k); } catch (e) { return null; } }, set: function (k) { try { localStorage.setItem(k, "1"); } catch (e) {} } };

  var root = document.createElement("div");
  root.setAttribute("data-fleetwatch-status", "");
  root.style.cssText = "position:fixed;z-index:2147483000;bottom:16px;" + (pos === "bottom-left" ? "left:16px;" : "right:16px;") +
    "display:flex;flex-direction:column;gap:8px;align-items:" + (pos === "bottom-left" ? "flex-start" : "flex-end") + ";font:13px/1.4 system-ui,-apple-system,Segoe UI,Roboto,sans-serif;max-width:340px";
  var ready = function (f) { document.body ? f() : document.addEventListener("DOMContentLoaded", f); };
  ready(function () { document.body.appendChild(root); });

  function el(tag, css, text) { var e = document.createElement(tag); e.style.cssText = css; if (text) e.textContent = text; return e; }

  function open(p) {
    var now = Date.now();
    var list = (p.notices || []).filter(function (n) { return n.status !== "resolved" && n.start <= now && (!n.end || n.end > now); });
    return list[0];
  }

  function render(p) {
    root.innerHTML = "";
    var n = open(p);
    var key = n ? "fw-notice-" + n.start + "-" + n.title.length : "";
    if (n && mode !== "pill" && !store.get(key)) {
      var card = el("div", "background:" + bg + ";color:" + fg + ";border:1px solid " + line + ";border-left:4px solid " + (COLOR[n.status === "outage" ? "major" : n.status === "maintenance" ? "maintenance" : "degraded"]) +
        ";border-radius:12px;padding:12px 14px;box-shadow:0 8px 24px rgba(0,0,0,.12);position:relative");
      card.appendChild(el("div", "font-weight:600;padding-right:18px", n.title));
      var last = (n.updates && n.updates[0] && n.updates[0].body) || n.body;
      if (last) card.appendChild(el("div", "color:" + muted + ";margin-top:4px", last.length > 180 ? last.slice(0, 180) + "…" : last));
      var more = el("a", "display:inline-block;margin-top:6px;color:" + fg + ";font-weight:600;text-decoration:underline", "Details");
      more.href = link; more.target = "_blank"; more.rel = "noopener";
      card.appendChild(more);
      var x = el("button", "position:absolute;top:8px;right:10px;border:0;background:none;color:" + muted + ";cursor:pointer;font-size:16px", "×");
      x.setAttribute("aria-label", "Dismiss");
      x.onclick = function () { store.set(key); card.remove(); };
      card.appendChild(x);
      root.appendChild(card);
    }
    if (mode !== "banner") {
      var a = el("a", "display:inline-flex;align-items:center;gap:8px;background:" + bg + ";color:" + fg + ";border:1px solid " + line +
        ";border-radius:999px;padding:7px 12px;text-decoration:none;box-shadow:0 4px 14px rgba(0,0,0,.10)");
      a.href = link; a.target = "_blank"; a.rel = "noopener";
      a.appendChild(el("span", "width:8px;height:8px;border-radius:50%;background:" + (COLOR[p.overall] || COLOR.operational)));
      a.appendChild(el("span", "", LABEL[p.overall] || LABEL.operational));
      root.appendChild(a);
    }
    if (n) tellChat(n, key);
  }

  // Visitors chatting with you hear it from the chat too: once per notice each.
  function tellChat(n, key) {
    if (chat === "none" || store.get(key + "-chat") || n.status === "info") return;
    var last = (n.updates && n.updates[0] && n.updates[0].body) || n.body || "";
    var text = "We know about the problem: " + n.title + "." + (last ? " " + last : "") + " Updates: " + link;
    if ((chat === "auto" || chat === "crisp") && window.$crisp) {
      window.$crisp.push(["do", "message:show", ["text", text]]);
      store.set(key + "-chat");
    } else if ((chat === "auto" || chat === "intercom") && typeof window.Intercom === "function") {
      // An Intercom workflow triggered by this event shows your message (set up once in Intercom).
      window.Intercom("trackEvent", "status-incident", { title: n.title, status: n.status, update: last.slice(0, 250), url: link });
      store.set(key + "-chat");
    }
  }

  function load() {
    fetch(api, { credentials: "omit" }).then(function (r) { return r.ok ? r.json() : null; }).then(function (p) { if (p) ready(function () { render(p); }); }).catch(function () {});
  }
  load();
  setInterval(load, 60000);
})();
