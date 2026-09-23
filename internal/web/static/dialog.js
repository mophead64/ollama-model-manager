// Generic <dialog> opener, ported from QA Tracker. A button with
// data-dialog-open="<id>" opens that dialog modally; data-dialog-close (anywhere
// inside a dialog) closes it, as does a click on the backdrop.
//
// One dialog can serve many openers: each data-fill-<key>="value" on the opener
// is copied into the dialog's [data-fill="<key>"] elements (an input's value,
// anything else's text), e.g. the model name in a shared delete confirmation.
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
      if ("value" in el && el.tagName !== "BUTTON") el.value = opener.dataset[k];
      else el.textContent = opener.dataset[k];
    });
  });
}

document.addEventListener("DOMContentLoaded", function () {
  document.querySelectorAll("dialog[data-dialog-autoopen]").forEach(function (d) {
    if (!d.open && typeof d.showModal === "function") d.showModal();
  });
});
