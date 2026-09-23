// The Chat page: a model picker (a dropdown with keyboard support and a
// filter) and the conversation. The conversation lives here and is posted
// whole to /chat each turn, to whichever model is picked at the time; the
// reply streams back as newline-delimited JSON (see handlers_chat.go) and is
// shown as plain text.
(function () {
  var panel = document.getElementById("chat");
  if (!panel) return;

  // ---- Model picker -------------------------------------------------------

  var picker = document.querySelector("[data-picker]");
  var toggle = picker.querySelector("[data-picker-toggle]");
  var current = picker.querySelector("[data-picker-current]");
  var menu = picker.querySelector("[data-picker-menu]");
  var filter = picker.querySelector("[data-picker-filter]");
  var empty = picker.querySelector("[data-picker-empty]");
  var items = Array.prototype.slice.call(picker.querySelectorAll("[role=option]"));

  function visibleItems() { return items.filter(function (i) { return !i.hidden; }); }

  function setActive(item) {
    items.forEach(function (i) { i.classList.toggle("active", i === item); });
    if (item) {
      item.focus({ preventScroll: true });
      item.scrollIntoView({ block: "nearest" });
    }
  }

  function open() {
    menu.hidden = false;
    toggle.setAttribute("aria-expanded", "true");
    if (filter) {
      filter.value = "";
      applyFilter();
      filter.focus();
    } else {
      setActive(picker.querySelector("[aria-selected=true]") || items[0]);
    }
  }

  function close(refocus) {
    if (menu.hidden) return;
    menu.hidden = true;
    toggle.setAttribute("aria-expanded", "false");
    setActive(null);
    if (refocus) toggle.focus();
  }

  function applyFilter() {
    var q = filter.value.trim().toLowerCase();
    items.forEach(function (i) { i.hidden = q !== "" && !i.dataset.value.toLowerCase().includes(q); });
    empty.hidden = visibleItems().length > 0;
  }

  function move(step) {
    var vis = visibleItems();
    if (!vis.length) return;
    var at = vis.indexOf(picker.querySelector(".dropdown-item.active"));
    var next;
    if (at >= 0) next = Math.max(0, Math.min(vis.length - 1, at + step));
    else next = step > 0 ? 0 : vis.length - 1; // nothing active yet: start from the end moved towards
    setActive(vis[next]);
  }

  function choose(item) {
    var name = item.dataset.value;
    items.forEach(function (i) { i.setAttribute("aria-selected", String(i === item)); });
    current.innerHTML = item.innerHTML; // our own markup: the item's name and size
    close(false);

    panel.dataset.model = name;
    input.disabled = false;
    send.disabled = !!busy;
    input.placeholder = "Say something… (Enter to send, Shift+Enter for a new line)";
    try {
      var url = new URL(location.href);
      url.searchParams.set("model", name);
      history.replaceState(history.state, "", url.pathname + url.search);
    } catch (e) {
      // Only the address bar misses out: a reload won't reselect the model.
    }
    if (window.htmx) {
      htmx.ajax("GET", "/chat/model?name=" + encodeURIComponent(name), { target: "#chat-model-info", swap: "outerHTML" });
    }
    input.focus();
  }

  toggle.addEventListener("click", function () { menu.hidden ? open() : close(true); });
  toggle.addEventListener("keydown", function (e) {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") { e.preventDefault(); open(); }
  });
  menu.addEventListener("click", function (e) {
    var item = e.target.closest("[role=option]");
    if (item) choose(item);
  });
  menu.addEventListener("keydown", function (e) {
    switch (e.key) {
      case "ArrowDown": e.preventDefault(); move(1); break;
      case "ArrowUp": e.preventDefault(); move(-1); break;
      case "Home": if (e.target !== filter) { e.preventDefault(); setActive(visibleItems()[0]); } break;
      case "End": if (e.target !== filter) { e.preventDefault(); var v = visibleItems(); setActive(v[v.length - 1]); } break;
      case "Enter":
        e.preventDefault();
        var active = picker.querySelector(".dropdown-item.active") || (filter && visibleItems().length === 1 && visibleItems()[0]);
        if (active) choose(active);
        break;
      case "Escape": e.preventDefault(); close(true); break;
      case "Tab": close(false); break;
    }
  });
  if (filter) {
    filter.addEventListener("input", function () {
      applyFilter();
      setActive(null);
      filter.focus();
    });
  }
  document.addEventListener("click", function (e) { if (!picker.contains(e.target)) close(false); });

  // ---- Conversation ------------------------------------------------------

  var log = panel.querySelector("[data-chat-log]");
  var form = panel.querySelector("[data-chat-form]");
  var input = form.querySelector("textarea");
  var send = panel.querySelector("[data-chat-send]");
  var stop = panel.querySelector("[data-chat-stop]");
  var clear = panel.querySelector("[data-chat-clear]");

  var messages = []; // {role, content}, as sent to Ollama
  var busy = null;   // AbortController of the reply in progress

  function el(tag, cls, text) {
    var n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text) n.textContent = text;
    return n;
  }
  function scroll() { log.scrollTop = log.scrollHeight; }

  function setBusy(ctrl) {
    busy = ctrl;
    send.disabled = !!ctrl || !panel.dataset.model;
    stop.hidden = !ctrl;
    clear.hidden = !!ctrl || messages.length === 0;
  }

  // The line under a reply: which model gave it, and how fast.
  function statsLine(model, s) {
    var line = el("div", "msg-stats");
    line.appendChild(el("span", "model-tag", model));
    if (s) {
      var parts = [s.tokens + " tokens"];
      if (s.tokens_per_sec) parts.push(s.tokens_per_sec.toFixed(1) + " tokens/s");
      if (s.load_ms >= 500) parts.push("loaded in " + (s.load_ms / 1000).toFixed(1) + "s");
      parts.push((s.total_ms / 1000).toFixed(1) + "s in total");
      line.appendChild(document.createTextNode(" · " + parts.join(" · ")));
    }
    return line;
  }

  function ask(text) {
    var model = panel.dataset.model;
    messages.push({ role: "user", content: text });
    log.appendChild(el("div", "msg user", text));

    var bubble = el("div", "msg assistant");
    var pending = el("span", "pending", "Thinking… (loading the model first can take a while)");
    var thinking = null, thinkingText = null;
    var answer = document.createTextNode("");
    bubble.appendChild(pending);
    bubble.appendChild(answer);
    log.appendChild(bubble);
    scroll();

    var reply = "";
    var ctrl = new AbortController();
    setBusy(ctrl);

    function fail(msg) {
      bubble.remove();
      log.appendChild(el("div", "msg error", msg));
      messages.pop(); // let them edit and resend
      input.value = input.value || text;
      scroll();
    }

    fetch("/chat", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ model: model, messages: messages }),
      signal: ctrl.signal,
      credentials: "same-origin"
    }).then(function (resp) {
      if (resp.status === 401) { location.reload(); throw new Error("signed out"); }
      if (!resp.ok || !resp.body) throw new Error("the request failed (" + resp.status + ")");
      var reader = resp.body.getReader(), decoder = new TextDecoder(), buf = "", failed = false;
      function handle(line) {
        if (!line.trim()) return;
        var ev = JSON.parse(line);
        if (ev.error) { failed = true; fail("Couldn't get a reply from " + model + ": " + ev.error); return; }
        if (pending.parentNode) pending.remove();
        if (ev.thinking) {
          if (!thinking) {
            thinking = el("details", "thinking");
            thinking.appendChild(el("summary", "", "Thinking"));
            thinkingText = document.createTextNode("");
            thinking.appendChild(thinkingText);
            bubble.insertBefore(thinking, answer);
          }
          thinkingText.appendData(ev.thinking);
        }
        if (ev.content) { reply += ev.content; answer.appendData(ev.content); }
        if (ev.done) log.appendChild(statsLine(model, ev.stats));
        scroll();
      }
      function pump() {
        return reader.read().then(function (r) {
          if (r.done) { handle(buf); return failed; }
          buf += decoder.decode(r.value, { stream: true });
          var lines = buf.split("\n");
          buf = lines.pop();
          lines.forEach(handle);
          return pump();
        });
      }
      return pump();
    }).then(function (failed) {
      if (!failed) messages.push({ role: "assistant", content: reply });
    }).catch(function (err) {
      if (err.name === "AbortError") {
        // Stopped: keep what arrived, so the conversation can carry on from it.
        if (pending.parentNode) pending.remove();
        answer.appendData(reply ? " …" : "(stopped)");
        log.appendChild(statsLine(model, null));
        messages.push({ role: "assistant", content: reply });
      } else if (err.message !== "signed out") {
        fail("Couldn't get a reply from " + model + ": " + err.message);
      }
    }).then(function () {
      setBusy(null);
      input.focus();
    });
  }

  form.addEventListener("submit", function (e) {
    e.preventDefault();
    var text = input.value.trim();
    if (!text || busy || !panel.dataset.model) return;
    input.value = "";
    ask(text);
  });
  input.addEventListener("keydown", function (e) {
    if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
      e.preventDefault();
      form.requestSubmit();
    }
  });
  stop.addEventListener("click", function () { if (busy) busy.abort(); });
  clear.addEventListener("click", function () {
    messages = [];
    log.textContent = "";
    setBusy(null);
    input.focus();
  });
  // Stop generating if the page is left mid-reply.
  window.addEventListener("pagehide", function () { if (busy) busy.abort(); });

  // Arriving with a model already picked (a Chat button elsewhere): ready to
  // type. After load, so nothing else takes the focus back.
  if (panel.dataset.model) {
    window.addEventListener("load", function () { setTimeout(function () { input.focus(); }, 0); });
  }
})();
