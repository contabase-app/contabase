(function() {
  'use strict';

  if (window.__contabaseAdminAccessibility) return;
  window.__contabaseAdminAccessibility = true;

  function focusElement(el) {
    if (!el || typeof el.focus !== 'function') return;
    if (!el.hasAttribute('tabindex')) el.setAttribute('tabindex', '-1');
    el.focus();
  }

  function syncScrollHints(root) {
    var scope = root || document;
    scope.querySelectorAll('.admin-table-wrap').forEach(function(wrap) {
      var hint = wrap.parentElement
        ? wrap.parentElement.querySelector('.admin-scroll-hint')
        : null;
      if (!hint) return;
      hint.classList.toggle('is-visible', wrap.scrollWidth > wrap.clientWidth + 1);
    });
  }

  function init(root) {
    syncScrollHints(root);
    setupConfirmObserver();
  }

  // Confirmações destrutivas administrativas usam o modal global. O foco é
  // gerenciado aqui, sem alterar o overlay-stack nem o sistema global.
  var confirmReturnFocus = null;
  var confirmTrapActive = false;
  var confirmObserver = null;
  var confirmObservedModal = null;

  function confirmModal() {
    return document.getElementById('global-confirm-modal');
  }

  function confirmIsOpen(modal) {
    return !!modal && !modal.classList.contains('hidden');
  }

  // O modal-interceptor consome Escape antes dos listeners de documento.
  // Observar a classe garante o retorno de foco mesmo quando o fechamento
  // vier do overlay-stack, do botão Cancelar, do backdrop ou de um swap.
  function setupConfirmObserver() {
    var modal = confirmModal();
    if (!modal || !window.MutationObserver) return;
    if (confirmObserver && confirmObservedModal === modal) return;
    if (confirmObserver) confirmObserver.disconnect();
    confirmObservedModal = modal;
    confirmObserver = new window.MutationObserver(function() {
      if (confirmTrapActive && !confirmIsOpen(modal)) restoreConfirmFocusIfClosed();
    });
    confirmObserver.observe(modal, { attributes: true, attributeFilter: ['class'] });
  }

  function confirmFocusables(modal) {
    return Array.prototype.slice.call(
      modal.querySelectorAll('button, a[href], input, select, textarea, [tabindex]:not([tabindex="-1"])')
    ).filter(function(el) {
      return !el.disabled && el.offsetParent !== null;
    });
  }

  function restoreConfirmFocusIfClosed() {
    var modal = confirmModal();
    if (confirmIsOpen(modal)) return;
    if (modal) modal.classList.remove('admin-confirm-open');
    var target = confirmReturnFocus;
    confirmReturnFocus = null;
    confirmTrapActive = false;
    if (target && document.contains(target) && typeof target.focus === 'function') {
      target.focus({ preventScroll: true });
    }
  }

  document.body.addEventListener('htmx:confirm', function(event) {
    var elt = event.detail && event.detail.elt;
    var payload = document.getElementById('settings-dynamic-payload');
    if (!elt || !payload || (!payload.contains(elt) && elt !== payload)) return;

    confirmReturnFocus = document.activeElement && document.activeElement !== document.body
      ? document.activeElement
      : elt;
    confirmTrapActive = true;
    setupConfirmObserver();
    window.requestAnimationFrame(function() {
      var modal = confirmModal();
      if (!confirmIsOpen(modal)) return;
      modal.classList.add('admin-confirm-open');
      var cancel = modal.querySelector('[data-onclick="cancelGlobalConfirm"]');
      if (cancel && typeof cancel.focus === 'function') cancel.focus({ preventScroll: true });
    });
  });

  document.addEventListener('keydown', function(event) {
    if (event.key === 'Escape' && confirmTrapActive) {
      window.requestAnimationFrame(restoreConfirmFocusIfClosed);
      return;
    }
    if (event.key !== 'Tab' || !confirmTrapActive) return;

    var modal = confirmModal();
    if (!confirmIsOpen(modal)) {
      restoreConfirmFocusIfClosed();
      return;
    }

    var items = confirmFocusables(modal);
    if (!items.length) return;
    var first = items[0];
    var last = items[items.length - 1];
    var active = document.activeElement;
    if (event.shiftKey && (active === first || !modal.contains(active))) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && (active === last || !modal.contains(active))) {
      event.preventDefault();
      first.focus();
    }
  });

  document.addEventListener('click', function(event) {
    if (!confirmTrapActive) return;
    if (event.target.closest && event.target.closest('#global-confirm-modal')) {
      window.requestAnimationFrame(restoreConfirmFocusIfClosed);
    }
  });

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function() { init(document); });
  } else {
    init(document);
  }

  document.body.addEventListener('htmx:afterSwap', function(event) {
    init(document);

    var target = event.detail && event.detail.target;
    var payload = document.getElementById('settings-dynamic-payload');
    if (!target || !payload) return;
    if (target.id === 'auditoria-table-body') return;
    if (target !== payload && !payload.contains(target)) return;

    var feedback = target.querySelector('[role="alert"], [role="status"]');
    if (!feedback && target !== payload) {
      feedback = target.closest('[role="alert"], [role="status"]');
    }
    if (!feedback) feedback = payload.querySelector('[role="alert"], [role="status"]');
    if (feedback) {
      focusElement(feedback);
      return;
    }

    var heading = payload.querySelector('h1');
    if (heading) focusElement(heading);
  });

  window.addEventListener('resize', function() {
    syncScrollHints(document);
  }, { passive: true });
})();
