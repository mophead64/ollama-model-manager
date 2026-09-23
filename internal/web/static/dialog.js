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
// A <dialog data-dialog-autoopen> is opened as soon as the page loads: used when
// a form inside it posts, fails server-side, and the page re-renders with the
// error, so the user sees it without reopening the dialog.

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

  document.querySelectorAll("dialog[data-dialog-autoopen]").forEach(function (d) {
    if (!d.open && typeof d.showModal === "function") d.showModal();
  });
});

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
