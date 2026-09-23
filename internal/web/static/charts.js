// Live system-load graphs for the System page. Each .chart-card[data-metric]
// inside [data-charts] gets a percentage line chart of that metric, redrawn
// from the JSON at data-charts every sample interval while the tab is visible.
// Hover (or focus + arrow keys) shows a crosshair and the value at that time.
(function () {
  var root = document.querySelector("[data-charts]");
  if (!root) return;
  var src = root.getAttribute("data-charts");
  var NS = "http://www.w3.org/2000/svg";
  var H = 160, M = { top: 8, right: 8, bottom: 20, left: 38 };
  var data = null;
  var cards = Array.prototype.map.call(root.querySelectorAll("[data-metric]"), setup);

  function el(name, attrs, parent) {
    var n = document.createElementNS(NS, name);
    for (var k in attrs) n.setAttribute(k, attrs[k]);
    if (parent) parent.appendChild(n);
    return n;
  }

  function fmtPct(v) { return v.toFixed(1) + "%"; }
  // Matches the server's formatBytes: binary units, one decimal place.
  function fmtBytes(n) {
    if (n < 1024) return n + " B";
    var units = "KMGTPE", i = -1;
    do { n /= 1024; i++; } while (n >= 1024 && i < units.length - 1);
    return n.toFixed(1) + " " + units[i] + "B";
  }
  function fmtTime(t) {
    return new Date(t).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
  }

  function setup(card) {
    var c = {
      card: card,
      key: card.getAttribute("data-metric"),
      label: card.getAttribute("data-label"),
      plot: card.querySelector("[data-plot]"),
      now: card.querySelector("[data-now]"),
      empty: card.querySelector("[data-empty]"),
      hover: null, // index of the sample under the crosshair, or null
      pts: []
    };
    c.tip = document.createElement("div");
    c.tip.className = "chart-tip";
    c.tip.hidden = true;
    c.tipVal = document.createElement("strong");
    c.tipBytes = document.createElement("span"); // memory charts only: "9.8 GB of 24.0 GB"
    c.tipBytes.className = "chart-tip-bytes";
    c.tipTime = document.createElement("span");
    c.tip.appendChild(c.tipVal);
    c.tip.appendChild(c.tipBytes);
    c.tip.appendChild(c.tipTime);
    card.appendChild(c.tip);

    c.svg = el("svg", { role: "img", tabindex: "0" }, c.plot);
    c.base = el("g", {}, c.svg);
    c.area = el("path", { class: "area" }, c.svg);
    c.line = el("path", { class: "line" }, c.svg);
    c.end = el("circle", { class: "dot", r: 4 }, c.svg);
    c.xhair = el("line", { class: "xhair", visibility: "hidden" }, c.svg);
    c.hdot = el("circle", { class: "dot", r: 4, visibility: "hidden" }, c.svg);

    // The crosshair snaps to the sample nearest the pointer's x.
    c.svg.addEventListener("pointermove", function (e) {
      if (!c.pts.length) return;
      var x = e.clientX - c.svg.getBoundingClientRect().left;
      var best = 0;
      for (var i = 1; i < c.pts.length; i++) {
        if (Math.abs(c.pts[i].x - x) < Math.abs(c.pts[best].x - x)) best = i;
      }
      c.hover = c.pts[best].i;
      showHover(c);
    });
    c.svg.addEventListener("pointerleave", function () { c.hover = null; showHover(c); });
    c.svg.addEventListener("blur", function () { c.hover = null; showHover(c); });
    c.svg.addEventListener("keydown", function (e) {
      if (!c.pts.length || (e.key !== "ArrowLeft" && e.key !== "ArrowRight")) return;
      e.preventDefault();
      var pos = c.pts.findIndex(function (p) { return p.i === c.hover; });
      if (pos < 0) pos = c.pts.length - 1;
      else pos = Math.max(0, Math.min(c.pts.length - 1, pos + (e.key === "ArrowLeft" ? -1 : 1)));
      c.hover = c.pts[pos].i;
      showHover(c);
    });
    return c;
  }

  function draw(c) {
    var samples = data.samples;
    var W = c.plot.clientWidth || 300;
    var pw = W - M.left - M.right, ph = H - M.top - M.bottom;
    c.svg.setAttribute("viewBox", "0 0 " + W + " " + H);
    c.W = W; c.ph = ph;

    var has = samples.some(function (s) { return s[c.key] != null; });
    c.plot.hidden = !has;
    c.empty.hidden = has;
    if (!has) {
      c.now.textContent = "—";
      c.pts = [];
      return;
    }

    // Fixed window ending at the newest sample, so the graph scrolls steadily.
    var tEnd = samples[samples.length - 1].t, tStart = tEnd - data.window_ms;
    function X(t) { return M.left + pw * (t - tStart) / (tEnd - tStart); }
    function Y(v) { return M.top + ph * (1 - v / 100); }

    // Hairline grid at 0/50/100% plus the time-axis labels.
    while (c.base.firstChild) c.base.removeChild(c.base.firstChild);
    [0, 50, 100].forEach(function (v) {
      el("line", { class: "grid", x1: M.left, x2: W - M.right, y1: Y(v), y2: Y(v) }, c.base);
      el("text", { class: "tick", x: M.left - 6, y: Y(v) + 4, "text-anchor": "end" }, c.base).textContent = v + "%";
    });
    var mins = Math.round(data.window_ms / 60000);
    el("text", { class: "tick", x: M.left, y: H - 4 }, c.base).textContent = mins + " min ago";
    el("text", { class: "tick", x: W - M.right, y: H - 4, "text-anchor": "end" }, c.base).textContent = "now";

    // Line and area, broken wherever a sample is missing.
    var line = "", area = "", run = [];
    c.pts = [];
    function flush() {
      if (!run.length) return;
      line += "M" + run.join("L");
      area += "M" + run[0].split(",")[0] + "," + Y(0) + "L" + run.join("L") +
        "L" + run[run.length - 1].split(",")[0] + "," + Y(0) + "Z";
      run = [];
    }
    samples.forEach(function (s, i) {
      var v = s[c.key];
      if (v == null) { flush(); return; }
      var x = X(s.t), y = Y(v);
      run.push(x.toFixed(1) + "," + y.toFixed(1));
      c.pts.push({ i: i, x: x, y: y, v: v, t: s.t, used: s[c.key + "_used"], total: s[c.key + "_total"] });
    });
    flush();
    c.line.setAttribute("d", line);
    c.area.setAttribute("d", area);

    var last = c.pts[c.pts.length - 1];
    c.end.setAttribute("cx", last.x);
    c.end.setAttribute("cy", last.y);
    c.now.textContent = fmtPct(last.v);

    var peak = Math.max.apply(null, c.pts.map(function (p) { return p.v; }));
    c.svg.setAttribute("aria-label", c.label + " over the last " + mins + " minutes: now " +
      fmtPct(last.v) + ", peak " + fmtPct(peak) + ". Use the arrow keys to read earlier values.");

    // Keep the crosshair on the same moment as the window scrolls, until it scrolls off.
    if (c.hover != null && !c.pts.some(function (p) { return p.i === c.hover; })) c.hover = null;
    showHover(c);
  }

  function showHover(c) {
    var p = c.hover == null ? null : c.pts.find(function (q) { return q.i === c.hover; });
    var vis = p ? "visible" : "hidden";
    c.xhair.setAttribute("visibility", vis);
    c.hdot.setAttribute("visibility", vis);
    c.tip.hidden = !p;
    if (!p) return;
    c.xhair.setAttribute("x1", p.x); c.xhair.setAttribute("x2", p.x);
    c.xhair.setAttribute("y1", M.top); c.xhair.setAttribute("y2", M.top + c.ph);
    c.hdot.setAttribute("cx", p.x); c.hdot.setAttribute("cy", p.y);
    c.tipVal.textContent = fmtPct(p.v);
    c.tipBytes.hidden = !p.total;
    c.tipBytes.textContent = p.total ? fmtBytes(p.used || 0) + " of " + fmtBytes(p.total) : "";
    c.tipTime.textContent = c.label + " · " + fmtTime(p.t);

    // Place the tooltip beside the crosshair, flipping left near the right edge.
    var plotBox = c.plot.getBoundingClientRect(), cardBox = c.card.getBoundingClientRect();
    var left = plotBox.left - cardBox.left + p.x + 12;
    if (p.x > c.W * 0.65) left -= c.tip.offsetWidth + 24;
    c.tip.style.left = left + "px";
    c.tip.style.top = (plotBox.top - cardBox.top + M.top) + "px";
  }

  function redraw() { if (data) cards.forEach(draw); }

  var timer = null;
  function poll() {
    clearTimeout(timer);
    if (document.visibilityState !== "visible") return; // resumes on visibilitychange
    fetch(src, { headers: { Accept: "application/json" }, credentials: "same-origin" })
      .then(function (r) {
        if (r.status === 401 || r.redirected) { location.reload(); throw new Error("signed out"); }
        return r.json();
      })
      .then(function (d) {
        data = d;
        root.style.opacity = "";
        redraw();
      })
      .catch(function () { root.style.opacity = ".6"; }) // keep the last frame, dimmed
      .then(function () {
        timer = setTimeout(poll, (data && data.interval_ms) || 2000);
      });
  }

  document.addEventListener("visibilitychange", poll);
  window.addEventListener("resize", redraw);
  poll();
})();
