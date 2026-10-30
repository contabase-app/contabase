(function() {
  'use strict';

  if (window.__contabaseAdminUserWorkspaceRoles) return;
  window.__contabaseAdminUserWorkspaceRoles = true;

  function syncRow(row) {
    var toggle = row.querySelector('[data-admin-user-workspace-toggle]');
    var roleSelect = row.querySelector('[data-admin-user-workspace-role]');
    if (!toggle || !roleSelect) return;

    roleSelect.disabled = !toggle.checked;
  }

  function init(root) {
    root = root || document;
    root.querySelectorAll('[data-admin-user-workspace-row]:not([data-uwr-ready])').forEach(function(row) {
      row.dataset.uwrReady = 'true';
      var toggle = row.querySelector('[data-admin-user-workspace-toggle]');
      if (!toggle) return;

      syncRow(row);
      toggle.addEventListener('change', function() {
        syncRow(row);
      });
    });
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function() { init(document); });
  } else {
    init(document);
  }

  document.body.addEventListener('htmx:afterSwap', function() { init(document); });
})();
