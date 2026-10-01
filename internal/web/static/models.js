// The Models page's selection, for acting on several models at once (the
// bulk bar: Unload, Delete…). The table is re-rendered by htmx as filters,
// sorting, paging and model state change, so the selection lives here, by
// model name, and is put back on the checkboxes after each swap. Models that
// drop out of the filter are dropped from it too, so an action never reaches
// a model that's no longer shown as selected.
(function () {
  var bar = document.getElementById("bulk-bar");
  if (!bar) return;

  var selected = new Set();
  var count = bar.querySelector("[data-bulk-count]");
  var all = bar.querySelector("[data-bulk-all]");
  var total = bar.querySelector("[data-bulk-total]");
  var unloadForm = bar.querySelector("[data-bulk-form]");
  var deleteBtn = bar.querySelector("[data-bulk-delete]");

  function results() { return document.getElementById("model-results"); }

  // Every model the current filter matches, on any page.
  function matching() {
    var r = results();
    try {
      return r && r.dataset.matching ? JSON.parse(r.dataset.matching) || [] : [];
    } catch (e) {
      return [];
    }
  }

  function boxes() {
    var r = results();
    return r ? Array.prototype.slice.call(r.querySelectorAll(".bulk-check")) : [];
  }

  function render() {
    var match = matching();
    // Keep only what the filter still shows.
    selected.forEach(function (n) { if (match.indexOf(n) < 0) selected.delete(n); });

    var bs = boxes();
    bs.forEach(function (b) { b.checked = selected.has(b.value); });
    var page = results() && results().querySelector("[data-bulk-page]");
    if (page) {
      var on = bs.filter(function (b) { return b.checked; }).length;
      page.checked = bs.length > 0 && on === bs.length;
      page.indeterminate = on > 0 && on < bs.length;
    }

    bar.hidden = selected.size === 0;
    count.textContent = selected.size;
    total.textContent = match.length;
    all.hidden = selected.size >= match.length || match.length <= bs.length;
  }

  function selectAll() {
    matching().forEach(function (n) { selected.add(n); });
    render();
  }

  document.addEventListener("change", function (e) {
    var t = e.target;
    if (t.classList.contains("bulk-check")) {
      if (t.checked) selected.add(t.value); else selected.delete(t.value);
      render();
    } else if (t.matches("[data-bulk-page]")) {
      boxes().forEach(function (b) { if (t.checked) selected.add(b.value); else selected.delete(b.value); });
      render();
    }
  });

  document.addEventListener("click", function (e) {
    if (e.target.closest("[data-bulk-select-all]")) selectAll();
    // The "+1 quant" badges link to the duplicates panel: open it too.
    if (e.target.closest("[data-open-dupes]")) {
      var d = document.getElementById("dupes");
      if (d) d.open = true;
    }
  });
  all.addEventListener("click", selectAll);
  bar.querySelector("[data-bulk-clear]").addEventListener("click", function () {
    selected.clear();
    render();
  });

  // The selected names as the forms send them, and where to come back to.
  function fill(form) {
    form.querySelectorAll("input[data-bulk-name]").forEach(function (i) { i.remove(); });
    selected.forEach(function (n) {
      var i = document.createElement("input");
      i.type = "hidden";
      i.name = "name";
      i.value = n;
      i.setAttribute("data-bulk-name", "");
      form.appendChild(i);
    });
  }
  unloadForm.addEventListener("submit", function () { fill(unloadForm); });

  // Opens the Delete dialog for names: the selection, or a model's tags (the
  // duplicates panel's Delete, for a model listed under more than one).
  function openBulkDelete(names) {
    var dialog = document.getElementById("bulk-delete-modal");
    var body = document.getElementById("bulk-delete-body");
    if (!dialog) return;
    var q = new URLSearchParams();
    names.forEach(function (n) { q.append("name", n); });
    q.set("return", location.pathname + location.search);
    body.innerHTML = '<p class="muted">Loading…</p>';
    dialog.showModal();
    fetch("/models/bulk/confirm?" + q.toString(), { credentials: "same-origin" }).then(function (resp) {
      if (resp.status === 401) { location.reload(); throw new Error("signed out"); }
      return resp.text();
    }).then(function (html) {
      body.innerHTML = html; // our own server-rendered markup
    }).catch(function (err) {
      if (err.message === "signed out") return;
      body.innerHTML = "";
      var p = document.createElement("p");
      p.className = "error-box";
      p.textContent = "Couldn't reach the server: " + err.message;
      body.appendChild(p);
    });
  }
  if (deleteBtn) deleteBtn.addEventListener("click", function () { openBulkDelete(Array.from(selected)); });
  document.addEventListener("click", function (e) {
    var b = e.target.closest("[data-delete-names]");
    if (b) openBulkDelete(JSON.parse(b.dataset.deleteNames));
  });

  document.body.addEventListener("htmx:afterSwap", render);
  render();
})();
