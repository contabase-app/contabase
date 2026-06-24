(function() {
  function initLogin2FA() {
    var toggleBtn = document.getElementById('recovery-toggle');
    var recoveryField = document.getElementById('recovery-field');
    if (!toggleBtn || !recoveryField) return;
    toggleBtn.addEventListener('click', function() {
      recoveryField.classList.toggle('hidden');
      var input = recoveryField.querySelector('input');
      if (input && !recoveryField.classList.contains('hidden')) {
        input.focus();
      }
    });
  }
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', initLogin2FA);
  } else {
    initLogin2FA();
  }
})();
