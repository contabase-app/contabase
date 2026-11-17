(function() {
  'use strict';

  if (window.__contabaseAdminUpdates) return;
  window.__contabaseAdminUpdates = true;

  function commandText(target) {
    if (!target) return '';
    return (target.textContent || '').replace(/\s+/g, ' ').trim();
  }

  function fallbackCopy(text) {
    var area = document.createElement('textarea');
    area.value = text;
    area.className = 'admin-updates__copy-fallback';
    area.setAttribute('readonly', '');
    area.setAttribute('aria-hidden', 'true');
    document.body.appendChild(area);
    area.focus();
    area.select();

    var copied = false;
    try {
      copied = typeof document.execCommand === 'function' && document.execCommand('copy');
    } catch (error) {
      copied = false;
    }
    document.body.removeChild(area);
    return copied;
  }

  function writeClipboard(text) {
    if (navigator.clipboard && typeof navigator.clipboard.writeText === 'function') {
      try {
        return navigator.clipboard.writeText(text).catch(function() {
          if (fallbackCopy(text)) return undefined;
          throw new Error('copy failed');
        });
      } catch (error) {
        if (fallbackCopy(text)) return Promise.resolve();
        return Promise.reject(new Error('copy failed'));
      }
    }
    if (fallbackCopy(text)) return Promise.resolve();
    return Promise.reject(new Error('copy failed'));
  }

  function copyStatus(button) {
    var scope = button.closest ? button.closest('[data-copy-scope]') : null;
    if (!scope && button.parentElement) scope = button.parentElement;
    return scope ? scope.querySelector('[data-copy-status]') : null;
  }

  function handleCopy(event) {
    var button = event.target && event.target.closest
      ? event.target.closest('[data-copy-target]')
      : null;
    if (!button) return;

    var target = document.getElementById(button.getAttribute('data-copy-target'));
    var text = commandText(target);
    if (!text) return;

    event.preventDefault();
    var status = copyStatus(button);
    writeClipboard(text).then(function() {
      if (status) status.textContent = 'Comando copiado.';
      if (typeof button.focus === 'function') button.focus();
    }).catch(function() {
      if (status) status.textContent = 'Não foi possível copiar. Selecione o comando manualmente.';
      if (typeof button.focus === 'function') button.focus();
    });
  }

  document.addEventListener('click', handleCopy);
})();
