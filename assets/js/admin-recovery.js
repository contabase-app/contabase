(function() {
  'use strict';

  function setRecoveryAction(root, action) {
    root.dataset.recoveryAction = action;

    var panel = root.querySelector('[data-admin-recovery-panel]');
    if (panel) {
      if (action === '') {
        panel.classList.add('hidden');
      } else {
        panel.classList.remove('hidden');
      }
    }

    var infos = root.querySelectorAll('[data-admin-recovery-info]');
    for (var i = 0; i < infos.length; i++) {
      if (infos[i].getAttribute('data-admin-recovery-info') === action) {
        infos[i].classList.remove('hidden');
      } else {
        infos[i].classList.add('hidden');
      }
    }

    var forms = root.querySelectorAll('[data-admin-recovery-form]');
    for (var j = 0; j < forms.length; j++) {
      if (forms[j].getAttribute('data-admin-recovery-form') === action) {
        forms[j].classList.remove('hidden');
      } else {
        forms[j].classList.add('hidden');
        var emailInput = forms[j].querySelector('input[name="confirm_email"]');
        if (emailInput) emailInput.value = '';
      }
    }
  }

  function handleClick(e) {
    var actionBtn = e.target.closest('[data-admin-recovery-action]');
    if (actionBtn) {
      var root = actionBtn.closest('[data-admin-recovery-root]');
      if (!root) return;
      var action = actionBtn.getAttribute('data-admin-recovery-action');
      setRecoveryAction(root, action);
      return;
    }

    var cancelBtn = e.target.closest('[data-admin-recovery-cancel]');
    if (cancelBtn) {
      var root = cancelBtn.closest('[data-admin-recovery-root]');
      if (!root) return;
      setRecoveryAction(root, '');
      return;
    }
  }

  function handleKeydown(e) {
    if (e.key !== 'Escape') return;
    var activePanel = document.querySelector('[data-admin-recovery-panel]:not(.hidden)');
    if (activePanel) {
      var root = activePanel.closest('[data-admin-recovery-root]');
      if (root) setRecoveryAction(root, '');
    }
  }

  document.addEventListener('click', handleClick, false);
  document.addEventListener('keydown', handleKeydown, false);

  document.body.addEventListener('htmx:afterSwap', function() {
    var roots = document.querySelectorAll('[data-admin-recovery-root]');
    for (var i = 0; i < roots.length; i++) {
      if (!roots[i].dataset.recoveryAction || roots[i].dataset.recoveryAction !== '') {
        setRecoveryAction(roots[i], '');
      }
    }
  });
})();
