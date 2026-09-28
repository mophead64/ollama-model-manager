// The Testing tab's New test form: filtering the model list, "Select all" /
// "Clear", the selection count and limit, run presets, the prompt's
// character count, and a live summary of what the test involves. The form
// works without it; the server checks everything again.
(function () {
  var form = document.getElementById("test-form");
  if (!form) return;

  var maxModels = parseInt(form.dataset.maxModels, 10);
  var maxRepeats = parseInt(form.dataset.maxRepeats, 10);
  var rows = Array.prototype.slice.call(form.querySelectorAll(".test-model"));
  var boxes = rows.map(function (r) { return r.querySelector("input[type=checkbox]"); });
  var filter = form.querySelector("[data-model-filter]");
  var empty = form.querySelector("[data-model-empty]");
  var selectedCount = form.querySelector("[data-selected-count]");
  var prompt = form.querySelector("textarea[name=prompt]");
  var promptCount = form.querySelector("[data-prompt-count]");
  var repeats = form.querySelector("[data-repeats-input]");
  var repeatsHint = form.querySelector("[data-repeats-hint]");
  var presets = Array.prototype.slice.call(form.querySelectorAll("[data-repeats]"));
  var needy = Array.prototype.slice.call(form.querySelectorAll("[data-needs]"));
  needy.forEach(function (b) {
    var tip = b.parentNode.querySelector("[data-why]");
    b.dataset.idleTip = tip.textContent; // what it says when the button's ready
  });

  var summary = document.querySelector("[data-summary]");
  var sumRuns = summary && summary.querySelector("[data-sum-runs]");
  var sumEst = summary && summary.querySelector("[data-sum-est]");
  var sumSize = summary && summary.querySelector("[data-sum-size]");
  var sumWarn = summary && summary.querySelector("[data-sum-warn]");
  if (summary) summary.hidden = false;

  function plural(n, one, many) { return n + " " + (n === 1 ? one : many); }

  function fmtBytes(n) {
    var units = ["B", "KB", "MB", "GB", "TB"], i = 0;
    while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
    return (i === 0 ? n : n.toFixed(1)) + " " + units[i];
  }

  function fmtDuration(ms) {
    var s = Math.round(ms / 1000);
    if (s < 60) return Math.max(s, 1) + " s";
    var m = Math.round(s / 60);
    if (m < 60) return m + " min";
    var h = Math.floor(m / 60);
    return h + " h" + (m % 60 ? " " + (m % 60) + " min" : "");
  }

  function selected() { return rows.filter(function (r, i) { return boxes[i].checked; }); }

  function runsValue() {
    var n = Number(repeats.value);
    return Number.isInteger(n) && n >= 1 && n <= maxRepeats ? n : 0;
  }

  function update() {
    var picked = selected();
    var full = picked.length >= maxModels;
    // At the limit, the rest can't be ticked (rather than failing on submit).
    boxes.forEach(function (b) { b.disabled = full && !b.checked; });
    rows.forEach(function (r, i) { r.classList.toggle("limit", boxes[i].disabled); });
    selectedCount.textContent = picked.length + " of " + maxModels + " selected" + (full ? " (the most a test can have)" : "");

    promptCount.textContent = prompt.value.length.toLocaleString() + " / " + Number(prompt.maxLength).toLocaleString();

    var runs = runsValue();
    repeats.setAttribute("aria-invalid", runs ? "false" : "true");
    repeatsHint.textContent = runs ? "1 to " + maxRepeats : "Enter a whole number from 1 to " + maxRepeats + ".";
    repeatsHint.classList.toggle("error", !runs);
    presets.forEach(function (p) {
      var on = Number(p.dataset.repeats) === runs;
      p.classList.toggle("on", on);
      p.setAttribute("aria-pressed", String(on));
    });

    // Each button says, in its tooltip, the first thing it's waiting for.
    var missing = {
      models: picked.length ? "" : "Select at least one model.",
      prompt: prompt.value.trim() ? "" : "Enter a prompt.",
      runs: runs ? "" : "Set the runs per model, from 1 to " + maxRepeats + "."
    };
    needy.forEach(function (b) {
      var why = b.dataset.needs.split(" ").map(function (k) { return missing[k]; }).filter(Boolean)[0] || "";
      var wrap = b.parentNode, tip = wrap.querySelector("[data-why]");
      b.disabled = !!why;
      tip.textContent = why || b.dataset.idleTip;
      // Focusable while disabled, so keyboard users can find out why.
      if (why) wrap.setAttribute("tabindex", "0"); else wrap.removeAttribute("tabindex");
    });

    if (!summary) return;
    if (!picked.length) {
      sumRuns.textContent = "No models selected yet.";
      sumEst.textContent = sumSize.textContent = "";
      sumWarn.innerHTML = "";
      return;
    }
    var total = picked.length * (runs || 0);
    sumRuns.textContent = plural(picked.length, "model", "models") + " × " + plural(runs || 0, "run", "runs") + " = " + plural(total, "run", "runs");

    // From how long each model took in earlier tests.
    var known = 0, unknown = [];
    picked.forEach(function (r) {
      var est = Number(r.dataset.est);
      if (est > 0) known += est * (runs || 0);
      else unknown.push(r.dataset.short);
    });
    if (!unknown.length) sumEst.textContent = "About " + fmtDuration(known) + ", from earlier tests.";
    else if (known) sumEst.textContent = "About " + fmtDuration(known) + " for the models tested before, plus " + plural(unknown.length, "model", "models") + " with no timings yet.";
    else sumEst.textContent = "No estimate yet: " + (unknown.length === 1 ? "this model hasn't" : "these models haven't") + " been tested before.";

    var bytes = picked.reduce(function (n, r) { return n + Number(r.dataset.size); }, 0);
    sumSize.textContent = fmtBytes(bytes) + " of models, loaded one at a time.";

    sumWarn.innerHTML = "";
    picked.forEach(function (r) {
      if (!r.dataset.fit) return;
      var li = document.createElement("li");
      li.textContent = r.dataset.short + ": " + r.dataset.fit + ", so it'll run slower.";
      sumWarn.appendChild(li);
    });
  }

  function applyFilter() {
    var q = filter.value.trim().toLowerCase();
    var shown = 0;
    rows.forEach(function (r) {
      var match = !q || r.dataset.search.toLowerCase().indexOf(q) >= 0;
      r.hidden = !match;
      if (match) shown++;
    });
    empty.hidden = shown > 0;
  }

  form.addEventListener("change", update);
  form.addEventListener("input", function (e) {
    if (e.target === filter) applyFilter();
    else update();
  });
  // Enter in the filter box would submit the form.
  filter.addEventListener("keydown", function (e) { if (e.key === "Enter") e.preventDefault(); });

  form.querySelector("[data-select-all]").addEventListener("click", function () {
    // The shown ones, up to the limit.
    var room = maxModels - selected().length;
    rows.forEach(function (r, i) {
      if (room > 0 && !r.hidden && !boxes[i].checked) { boxes[i].checked = true; room--; }
    });
    update();
  });
  form.querySelector("[data-select-none]").addEventListener("click", function () {
    boxes.forEach(function (b) { b.checked = false; });
    update();
  });
  presets.forEach(function (p) {
    p.addEventListener("click", function () { repeats.value = p.dataset.repeats; update(); });
  });

  // Reset: back to a blank form, whatever it was filled in from. A template
  // or ?model= in the address would fill it in again on a refresh, so they go too.
  form.querySelector("[data-reset]").addEventListener("click", function (e) {
    e.preventDefault();
    prompt.value = "";
    form.querySelector("input[name=name]").value = "";
    boxes.forEach(function (b) { b.checked = false; });
    repeats.value = repeats.dataset.default;
    filter.value = "";
    applyFilter();
    update();
    try { history.replaceState(history.state, "", location.pathname); } catch (err) {}
    // Messages about what was filled in, or wrong with it, no longer apply.
    document.querySelectorAll('.notice[data-clear-param="template saved"]').forEach(function (n) { n.remove(); });
    form.closest(".panel").querySelectorAll(".error-box, .notice").forEach(function (n) { n.remove(); });
    prompt.focus();
  });

  update();
})();
