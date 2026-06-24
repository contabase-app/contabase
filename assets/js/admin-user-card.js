(function() {
  'use strict';

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

      function showEdit() {
        view.classList.add('hidden');
        form.classList.remove('hidden');
        card.classList.add.apply(card.classList, EDIT_CLASSES.split(' '));
      }

      function showView() {
        view.classList.remove('hidden');
        form.classList.add('hidden');
        card.classList.remove.apply(card.classList, EDIT_CLASSES.split(' '));
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
