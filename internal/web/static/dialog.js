// Generic <dialog> opener. A button with
// data-dialog-open="<id>" opens that dialog modally; data-dialog-close (anywhere
// inside a dialog) closes it, as does a click on the backdrop.
//
// One dialog can serve many openers: each data-fill-<key>="value" on the opener
// is copied into the dialog's [data-fill="<key>"] elements (an input's value,
// anything else's text; or the attribute named by data-fill-attr, e.g. a
// form's action), e.g. the model name in a shared delete confirmation.
//
// A [data-dismiss] button removes the .notice it's in.
//
// A submit button with data-busy="Loading…" is disabled and relabelled, with a
// spinner, while its form submits, for actions that take a while.
//
// A button with data-copy="<id>" copies that input's value to the clipboard;
// one with data-copy-text="<text>" copies that text. Inside a .copy-tip-wrap,
// "Copied" shows in its .copy-tip tooltip rather than on the button.
//
// An open <details class="split-menu"> dropdown closes on a click outside it,
// on Escape, or when one of its items is picked.
//
// A <dialog data-dialog-autoopen> is opened as soon as the page (or the htmx
// swap bringing it) loads: used when a form inside it posts, fails server-side,
// and the page re-renders with the error, so the user sees it without
// reopening the dialog.

document.addEventListener("click", function (e) {
  var opener = e.target.closest("[data-dialog-open]");
  if (opener && !opener.disabled) {
    var dialog = document.getElementById(opener.getAttribute("data-dialog-open"));
    if (dialog && !dialog.open && typeof dialog.showModal === "function") {
      fill(dialog, opener);
      dialog.showModal();
    }
    return;
  }

  var copier = e.target.closest("[data-copy], [data-copy-text]");
  if (copier) {
    copy(copier);
    return;
  }

  var dismiss = e.target.closest("[data-dismiss]");
  if (dismiss) {
    var box = dismiss.closest(".notice");
    if (box) box.remove();
    return;
  }

  var closer = e.target.closest("[data-dialog-close]");
  if (closer) {
    var d = closer.closest("dialog");
    if (d && d.open) d.close();
    return;
  }

  // A click on the dialog element itself (not its content) is on the ::backdrop.
  if (e.target.tagName === "DIALOG" && e.target.open) {
    e.target.close();
  }
});

document.addEventListener("click", function (e) {
  document.querySelectorAll("details.split-menu[open]").forEach(function (m) {
    if (!m.contains(e.target) || e.target.closest(".menu")) m.open = false;
  });
});

document.addEventListener("keydown", function (e) {
  if (e.key !== "Escape") return;
  document.querySelectorAll("details.split-menu[open]").forEach(function (m) {
    m.open = false;
    m.querySelector("summary").focus();
  });
});

function fill(dialog, opener) {
  Object.keys(opener.dataset).forEach(function (k) {
    if (k.indexOf("fill") !== 0 || k.length === 4) return;
    var key = k.charAt(4).toLowerCase() + k.slice(5); // dataset camelCases data-fill-foo-bar -> fillFooBar
    dialog.querySelectorAll('[data-fill="' + key + '"]').forEach(function (el) {
      var v = opener.dataset[k];
      if (el.dataset.fillAttr) el.setAttribute(el.dataset.fillAttr, v);
      else if ("value" in el && el.tagName !== "BUTTON" && el.tagName !== "FORM") el.value = v;
      else el.textContent = v;
    });
  });
}

document.addEventListener("DOMContentLoaded", function () {
  // A one-off notice (e.g. ?deleted=x) is shown once: drop its query param so
  // a refresh or bookmark doesn't bring it back.
  document.querySelectorAll(".notice[data-clear-param]").forEach(function (n) {
    try {
      var url = new URL(window.location.href);
      url.searchParams.delete(n.dataset.clearParam);
      history.replaceState(history.state, "", url.pathname + url.search + url.hash);
    } catch (e) {}
  });

  autoOpen(document);
});

// Content htmx swaps in can bring an autoopen dialog too (e.g. the discover
// page's "download anyway?" confirmation).
document.addEventListener("htmx:load", function (e) {
  autoOpen(e.detail.elt);
});

function autoOpen(root) {
  if (!root.querySelectorAll) return;
  var found = Array.prototype.slice.call(root.querySelectorAll("dialog[data-dialog-autoopen]"));
  if (root.matches && root.matches("dialog[data-dialog-autoopen]")) found.push(root);
  found.forEach(function (d) {
    if (!d.open && typeof d.showModal === "function") d.showModal();
  });
}

document.addEventListener("submit", function (e) {
  var btn = e.submitter && e.submitter.matches("[data-busy]") ? e.submitter : e.target.querySelector("button[data-busy]");
  if (!btn || e.defaultPrevented) return;
  // Disabled after this tick, so the button still counts as the submitter.
  setTimeout(function () {
    btn.dataset.idleText = btn.textContent;
    btn.textContent = btn.dataset.busy;
    btn.classList.add("busy");
    btn.disabled = true;
  }, 0);
});

// Coming back to a page via the back button restores it from cache as it was
// left, busy buttons included; put them back.
window.addEventListener("pageshow", function (e) {
  if (!e.persisted) return;
  document.querySelectorAll("button.busy").forEach(function (btn) {
    btn.textContent = btn.dataset.idleText || btn.textContent;
    btn.classList.remove("busy");
    btn.disabled = false;
  });
});

function copy(btn) {
  var input = btn.dataset.copyText === undefined && document.getElementById(btn.dataset.copy);
  if (!input && btn.dataset.copyText === undefined) return;
  var text = input ? input.value : btn.dataset.copyText;
  // Feedback goes in the button's tooltip when it has one, else the button.
  var wrap = btn.closest(".copy-tip-wrap");
  var label = (wrap && wrap.querySelector(".copy-tip")) || btn;
  function done(ok) {
    var idle = label.dataset.idleText || label.textContent;
    label.dataset.idleText = idle;
    label.textContent = ok ? "Copied" : "Select and copy it";
    if (wrap) wrap.classList.add("copied");
    clearTimeout(label._copyTimer);
    label._copyTimer = setTimeout(function () {
      label.textContent = idle;
      if (wrap) wrap.classList.remove("copied");
    }, 2000);
  }
  // The async clipboard API only exists on HTTPS pages (or localhost); the
  // app is often served over plain HTTP, where the old way still works.
  if (navigator.clipboard && window.isSecureContext) {
    if (input) input.select();
    navigator.clipboard.writeText(text).then(function () { done(true); }, function () { done(false); });
    return;
  }
  var temp;
  if (!input) {
    // execCommand copies the selection, so literal text needs something to select.
    temp = document.createElement("textarea");
    temp.value = text;
    temp.setAttribute("readonly", "");
    temp.style.position = "fixed";
    temp.style.opacity = "0";
    document.body.appendChild(temp);
  }
  (input || temp).select();
  var ok = false;
  try { ok = document.execCommand("copy"); } catch (e) {}
  if (temp) { temp.remove(); btn.focus(); }
  done(ok);
}
