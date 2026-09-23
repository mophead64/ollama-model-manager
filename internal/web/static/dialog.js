// Generic <dialog> opener, ported from QA Tracker. A button with
// data-dialog-open="<id>" opens that dialog modally; data-dialog-close (anywhere
// inside a dialog) closes it, as does a click on the backdrop.
//
// A <dialog data-dialog-autoopen> is opened as soon as the page loads: used when
// a form inside it posts, fails server-side, and the page re-renders with the
// error, so the user sees it without reopening the dialog.

document.addEventListener("click", function (e) {
  var opener = e.target.closest("[data-dialog-open]");
  if (opener && !opener.disabled) {
    var dialog = document.getElementById(opener.getAttribute("data-dialog-open"));
    if (dialog && !dialog.open && typeof dialog.showModal === "function") {
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

document.addEventListener("DOMContentLoaded", function () {
  document.querySelectorAll("dialog[data-dialog-autoopen]").forEach(function (d) {
    if (!d.open && typeof d.showModal === "function") d.showModal();
  });
});
