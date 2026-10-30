(function() {
  'use strict';

  if (window.__contabaseAdminUserCard) return;
  window.__contabaseAdminUserCard = true;

  var EDIT_CLASSES = 'ring-1 ring-violet-400/30 border-violet-400/30 dark:ring-violet-500/20 dark:border-violet-500/25';

  function init(root) {
    root = root || document;
    root.querySelectorAll('[data-admin-user-card]:not([data-uc-ready])').forEach(function(card) {
      card.dataset.ucReady = 'true';

      var view = card.querySelector('[data-admin-user-view]');
      var form = card.querySelector('[data-admin-user-form]');
      var editBtn = card.querySelector('[data-admin-user-edit]');
      var cancelBtn = card.querySelector('[data-admin-user-cancel]');

      if (!view || !form) return;

      function firstField() {
        var selectors = ['input', 'select', 'textarea'];
        for (var s = 0; s < selectors.length; s++) {
          var fields = form.querySelectorAll(selectors[s]);
          for (var f = 0; f < fields.length; f++) {
            var field = fields[f];
            var type = (field.getAttribute('type') || '').toLowerCase();
            if (type === 'hidden' || field.disabled) continue;
            return field;
          }
        }
        return null;
      }

      function showEdit() {
        view.classList.add('hidden');
        form.classList.remove('hidden');
        card.classList.add.apply(card.classList, EDIT_CLASSES.split(' '));
        if (editBtn) editBtn.setAttribute('aria-expanded', 'true');
        var field = firstField();
        if (field && typeof field.focus === 'function') field.focus();
      }

      function resetForm() {
        var formEl = form.querySelector('form') || form;
        if (formEl && typeof formEl.reset === 'function') formEl.reset();

        var toggles = form.querySelectorAll('[data-admin-user-workspace-toggle]');
        for (var t = 0; t < toggles.length; t++) {
          var row = toggles[t].closest('[data-admin-user-workspace-row]');
          var role = row ? row.querySelector('[data-admin-user-workspace-role]') : null;
          if (role) role.disabled = !toggles[t].checked;
        }
      }

      function showView() {
        view.classList.remove('hidden');
        form.classList.add('hidden');
        card.classList.remove.apply(card.classList, EDIT_CLASSES.split(' '));
        resetForm();
        if (editBtn) {
          editBtn.setAttribute('aria-expanded', 'false');
          if (typeof editBtn.focus === 'function') editBtn.focus();
        }
      }

      if (editBtn) editBtn.addEventListener('click', showEdit);
      if (cancelBtn) cancelBtn.addEventListener('click', showView);
    });
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function() { init(document); });
  } else {
    init(document);
  }

  document.body.addEventListener('htmx:afterSwap', function() { init(document); });
})();
