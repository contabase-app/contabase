(function() {
  'use strict';

  var sheetTrigger = null;
  var sheetTriggerDescriptor = null;
  var sheetFocusInstalled = false;
  var pendingMoneyValue = null;
  var pendingTabFocus = false;

  function closeCaixinha(dd) {
    dd.classList.remove('is-open');
    var panel = dd.querySelector('[data-ck-panel]');
    if (panel) panel.classList.add('hidden');
    var link = dd.closest('[data-meta-card]')?.querySelector('[data-meta-card-link]');
    if (link) {
      link.classList.remove('pointer-events-none');
      link.removeAttribute('aria-hidden');
    }
    var toggle = dd.querySelector('[data-ck-toggle]');
    if (toggle) toggle.setAttribute('aria-expanded', 'false');
    dd.dataset.ckMode = 'menu';
    dd.querySelectorAll('[data-ck-mode-panel]').forEach(function(p) {
      p.classList.toggle('hidden', p.dataset.ckModePanel !== 'menu');
    });
  }

  function setMode(dd, mode) {
    dd.dataset.ckMode = mode;
    dd.querySelectorAll('[data-ck-mode-panel]').forEach(function(p) {
      p.classList.toggle('hidden', p.dataset.ckModePanel !== mode);
    });
  }

  function openCaixinha(dd) {
    document.querySelectorAll('[data-caixinha-dropdown].is-open').forEach(function(other) {
      if (other !== dd) closeCaixinha(other);
    });
    var panel = dd.querySelector('[data-ck-panel]');
    if (!panel) return;
    dd.classList.add('is-open');
    panel.classList.remove('hidden');
    var link = dd.closest('[data-meta-card]')?.querySelector('[data-meta-card-link]');
    if (link) {
      link.classList.add('pointer-events-none');
      link.setAttribute('aria-hidden', 'true');
    }
    var toggle = dd.querySelector('[data-ck-toggle]');
    if (toggle) toggle.setAttribute('aria-expanded', 'true');
    setMode(dd, 'menu');
  }

  function sheetInContainer() {
    return document.querySelector('#bottom-sheet-container [data-metas-sheet]') ||
      document.querySelector('#bottom-sheet-container [data-metas-history]');
  }

  function isVisibleFocusable(el) {
    if (!el) return false;
    if (el.closest && el.closest('.hidden')) return false;
    if (el.hidden) return false;
    if (typeof window.getComputedStyle === 'function') {
      var style = window.getComputedStyle(el);
      if (style && (style.display === 'none' || style.visibility === 'hidden')) return false;
    }
    return true;
  }

  function installFocusTrap(sheet, panel) {
    if (!panel || typeof panel.addEventListener !== 'function') return;
    if (panel.dataset) {
      if (panel.dataset.sheetTrapReady === 'true') return;
      panel.dataset.sheetTrapReady = 'true';
    }
    panel.addEventListener('keydown', function(e) {
      if (e.key !== 'Tab' || e.ctrlKey || e.metaKey || e.altKey) return;
      // O seletor de ícones é um <dialog> nativo: enquanto estiver aberto, o
      // navegador controla o foco e o trap do sheet não deve interferir.
      if (sheet.querySelector('dialog[open]')) return;
      // O seletor de categoria é um overlay próprio (prioridade 120 no
      // overlay-stack). Enquanto ele estiver aberto, o foco pertence a ele.
      if (document.getElementById && document.getElementById('modalPickerBackdrop')) return;

      var nodes = sheet.querySelectorAll('button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])');
      var focusables = [];
      for (var i = 0; i < nodes.length; i++) {
        if (isVisibleFocusable(nodes[i])) focusables.push(nodes[i]);
      }
      if (!focusables.length) {
        e.preventDefault();
        return;
      }

      var first = focusables[0];
      var last = focusables[focusables.length - 1];
      var active = document.activeElement;
      var inside = sheet.contains(active);

      if (e.shiftKey && (active === first || !inside)) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && (active === last || !inside)) {
        e.preventDefault();
        first.focus();
      }
    });
  }

  function focusSheet() {
    var sheet = sheetInContainer();
    if (!sheet || sheet.dataset.sheetFocusReady === 'true') return;
    sheet.dataset.sheetFocusReady = 'true';

    var panel = sheet.querySelector('[data-metas-history-panel]') || sheet.querySelector('[data-metas-form-panel]');
    var close = sheet.querySelector('[data-metas-history-close]') || sheet.querySelector('.metas-sheet__close');
    var initial = sheet.querySelector('[data-metas-form-initial]');

    if (initial && isVisibleFocusable(initial) && typeof initial.focus === 'function') {
      initial.focus();
    } else if (close && typeof close.focus === 'function') {
      close.focus();
    } else if (panel && typeof panel.focus === 'function') {
      panel.focus();
    }

    installFocusTrap(sheet, panel || sheet);
  }

  function installSheetFocusBridge() {
    if (sheetFocusInstalled) return;
    if (typeof window.closeBottomSheet !== 'function' || window.__metasSheetFocusInstalled) {
      if (window.__metasSheetFocusInstalled) sheetFocusInstalled = true;
      return;
    }

    var originalClose = window.closeBottomSheet;
    window.closeBottomSheet = function() {
      var sheet = sheetInContainer();
      var trigger = resolveRestoreTrigger();
      var result = originalClose.apply(this, arguments);
      if (sheet) {
        sheetTrigger = null;
        sheetTriggerDescriptor = null;
        if (trigger && typeof trigger.focus === 'function') trigger.focus();
      }
      return result;
    };
    window.__metasSheetFocusInstalled = true;
    sheetFocusInstalled = true;
  }

  function resolveSheetTrigger(el) {
    if (!el || typeof el.closest !== 'function') return null;
    var link = el.closest('[data-sheet-trigger], a[href*="/metas/caixinha/historico"], a[href*="/metas/limite/historico"], [hx-get*="/metas/novo"], [hx-get*="/metas/caixinha/aporte/sheet"], [hx-get*="/metas/caixinha/resgate/sheet"]');
    if (!link) return null;
    var dropdown = link.closest('[data-caixinha-dropdown]');
    if (dropdown) {
      var toggle = dropdown.querySelector('[data-ck-toggle]');
      if (toggle) return toggle;
    }
    return link;
  }

  function describeTrigger(el) {
    if (!el) return null;
    if (el.id) return { type: 'id', value: el.id };
    var controls = el.getAttribute && el.getAttribute('aria-controls');
    if (controls) return { type: 'controls', value: controls };
    return null;
  }

  function resolveRestoreTrigger() {
    if (sheetTriggerDescriptor) {
      if (sheetTriggerDescriptor.type === 'id') {
        var byID = document.getElementById(sheetTriggerDescriptor.value);
        if (byID) return byID;
      }
      if (sheetTriggerDescriptor.type === 'controls' && typeof document.querySelector === 'function') {
        var safe = String(sheetTriggerDescriptor.value).replace(/["\\]/g, '\\$&');
        var byControls = document.querySelector('[aria-controls="' + safe + '"]');
        if (byControls) return byControls;
      }
    }
    if (sheetTrigger && sheetTrigger.isConnected !== false) return sheetTrigger;
    return null;
  }

  function captureSheetTrigger(el) {
    var trigger = resolveSheetTrigger(el);
    if (!trigger) return;
    sheetTrigger = trigger;
    sheetTriggerDescriptor = describeTrigger(trigger);
  }

  function activeMetasTab() {
    return document.querySelector('#metas-tabs [role="tab"][aria-selected="true"]');
  }

  function restorePendingTabFocus() {
    if (!pendingTabFocus) return;
    pendingTabFocus = false;
    var active = activeMetasTab();
    if (active && typeof active.focus === 'function') active.focus();
  }

  function initHistoryFocus() {
    installSheetFocusBridge();
    focusSheet();
  }

  function moneyIsPositive(value) {
    var digits = String(value || '').replace(/\D/g, '');
    return digits !== '' && Number(digits) > 0;
  }

  function clearMoneyError(form) {
    if (!form) return;
    var error = form.querySelector('.metas-form__field-error');
    if (error) {
      error.textContent = '';
      error.classList.add('hidden');
    }
    var input = form.querySelector('[name="amount"]');
    if (input) input.setAttribute('aria-invalid', 'false');
  }

  function validateMoneyForm(form) {
    var input = form.querySelector('[name="amount"]');
    if (!input) return true;
    if (moneyIsPositive(input.value)) {
      clearMoneyError(form);
      return true;
    }
    var error = form.querySelector('.metas-form__field-error');
    if (error) {
      error.textContent = 'Informe um valor maior que zero.';
      error.classList.remove('hidden');
    }
    input.setAttribute('aria-invalid', 'true');
    if (typeof input.focus === 'function') input.focus();
    return false;
  }

  function captureMoneyForm(form) {
    if (!form || !form.hasAttribute('data-metas-money-form')) return;
    var amount = form.querySelector('[name="amount"]');
    var box = form.querySelector('[name="box_id"]');
    pendingMoneyValue = {
      kind: form.getAttribute('data-metas-form-kind') || '',
      boxID: box ? box.value : '',
      value: amount ? amount.value : ''
    };
  }

  function restoreMoneyValue() {
    if (!pendingMoneyValue) return;
    var sheet = sheetInContainer();
    if (!sheet) {
      pendingMoneyValue = null;
      return;
    }
    var form = sheet.querySelector('form[data-metas-money-form][data-metas-form-kind="' + pendingMoneyValue.kind + '"]');
    if (!form) {
      pendingMoneyValue = null;
      return;
    }
    var box = form.querySelector('[name="box_id"]');
    if (box && pendingMoneyValue.boxID && box.value !== pendingMoneyValue.boxID) {
      pendingMoneyValue = null;
      return;
    }
    var amount = form.querySelector('[name="amount"]');
    if (amount && !amount.value && pendingMoneyValue.value) {
      amount.value = pendingMoneyValue.value;
      if (typeof amount.focus === 'function') amount.focus();
    }
    pendingMoneyValue = null;
  }

  function handleAfterSettle(event) {
    var target = event && event.detail && event.detail.target;
    var sheet = sheetInContainer();
    if (sheet) {
      focusSheet();
      restoreMoneyValue();
      return;
    }
    if (target && target.id === 'bottom-sheet-container' && target.innerHTML.trim() === '') {
      if (typeof window.overlayDidClose === 'function') window.overlayDidClose();
      if (sheetTrigger) {
        var trigger = resolveRestoreTrigger();
        sheetTrigger = null;
        sheetTriggerDescriptor = null;
        if (trigger && typeof trigger.focus === 'function') trigger.focus();
      }
    }
  }

  function init() {
    document.querySelectorAll('[data-caixinha-dropdown]:not([data-ck-ready])').forEach(function(dd) {
      dd.dataset.ckReady = 'true';

      var toggle = dd.querySelector('[data-ck-toggle]');
      var panel = dd.querySelector('[data-ck-panel]');
      if (!toggle || !panel) return;
      var link = dd.closest('[data-meta-card]')?.querySelector('[data-meta-card-link]');
      if (link) {
        link.classList.remove('pointer-events-none');
        link.removeAttribute('aria-hidden');
      }

      toggle.addEventListener('click', function(e) {
        e.preventDefault();
        e.stopPropagation();
        if (dd.classList.contains('is-open')) {
          closeCaixinha(dd);
        } else {
          openCaixinha(dd);
        }
      });

      dd.addEventListener('click', function(e) {
        var modeBtn = e.target.closest('button[data-ck-mode]');
        if (modeBtn) {
          e.stopPropagation();
          setMode(dd, modeBtn.dataset.ckMode);
          return;
        }
        var closeBtn = e.target.closest('[data-ck-close]');
        if (closeBtn) {
          closeCaixinha(dd);
          var hxEl = e.target.closest('[hx-get], [hx-post], [hx-put], [hx-delete], [hx-patch]');
          if (!hxEl) {
            e.stopPropagation();
          }
          return;
        }
        if (e.target.closest('form') || e.target.closest('input') || e.target.closest('label') || e.target.closest('button') || e.target.closest('select') || e.target.closest('textarea')) {
          e.stopPropagation();
          return;
        }
        e.stopPropagation();
        if (dd.classList.contains('is-open')) {
          closeCaixinha(dd);
        }
      });
    });

    initHistoryFocus();
  }

  document.addEventListener('click', function(e) {
    var tab = e.target.closest ? e.target.closest('#metas-tabs [role="tab"]') : null;
    if (tab) pendingTabFocus = true;
    captureSheetTrigger(e.target);
    if (e.target.closest('[data-caixinha-dropdown]')) return;
    document.querySelectorAll('[data-caixinha-dropdown].is-open').forEach(function(dd) {
      closeCaixinha(dd);
    });
  });

  document.addEventListener('keydown', function(e) {
    if (e.key === 'Escape') {
      document.querySelectorAll('[data-caixinha-dropdown].is-open').forEach(function(dd) {
        // Se o foco estava dentro do menu, ESC devolve o foco ao gatilho; o
        // fechamento por clique externo não rouba o foco do novo alvo.
        var hadFocus = dd.contains(document.activeElement);
        closeCaixinha(dd);
        if (hadFocus) {
          var toggle = dd.querySelector('[data-ck-toggle]');
          if (toggle) toggle.focus();
        }
      });
      return;
    }

    // As abas da página usam ativação manual: setas movem o foco; Enter/Espaço
    // acionam o HTMX nativo do botão. O tabindex é roving para não duplicar
    // paradas de Tab. O foco é restaurado após o swap de #metas-tabs.
    var tab = e.target.closest ? e.target.closest('#metas-tabs [role="tab"]') : null;
    if (!tab || e.ctrlKey || e.metaKey || e.altKey) return;
    var tabs = Array.prototype.slice.call(document.querySelectorAll('#metas-tabs [role="tab"]'));
    if (tabs.length < 2) return;
    var index = tabs.indexOf(tab);
    var next = null;
    if (e.key === 'ArrowRight') next = tabs[(index + 1) % tabs.length];
    else if (e.key === 'ArrowLeft') next = tabs[(index - 1 + tabs.length) % tabs.length];
    else if (e.key === 'Home') next = tabs[0];
    else if (e.key === 'End') next = tabs[tabs.length - 1];
    if (!next) return;
    e.preventDefault();
    tabs.forEach(function(item) {
      item.setAttribute('tabindex', item === next ? '0' : '-1');
    });
    next.focus();
  });

  // O seletor de ícones é um <dialog> nativo dentro do sheet. O overlay-stack
  // não o conhece; sem esta guarda, ESC fecharia o sheet junto com o diálogo.
  // A guarda roda em window/capture, antes do modal-interceptor, e não impede
  // o fechamento nativo do <dialog> (sem preventDefault).
  if (window.addEventListener) {
    window.addEventListener('keydown', function(e) {
      if (e.key !== 'Escape') return;
      var sheet = sheetInContainer();
      if (!sheet || !sheet.querySelector('dialog[open]')) return;
      e.stopImmediatePropagation();
    }, true);
  }

  document.addEventListener('submit', function(e) {
    var form = e.target;
    if (!form || !form.hasAttribute || !form.hasAttribute('data-metas-money-form')) return;
    if (validateMoneyForm(form)) return;
    e.preventDefault();
    e.stopImmediatePropagation();
  }, true);

  document.addEventListener('input', function(e) {
    var form = e.target.closest ? e.target.closest('form[data-metas-money-form]') : null;
    if (!form || e.target.name !== 'amount') return;
    clearMoneyError(form);
  });

  document.body.addEventListener('htmx:beforeRequest', function(e) {
    var elt = e.detail && e.detail.elt;
    if (!elt || typeof elt.closest !== 'function') return;
    captureMoneyForm(elt.closest('form[data-metas-money-form]'));
  });

  // Posiciona o chip de percentual sobre a barra: o CSP impede style= no
  // template e mutação de .style no JS, então o percentual chega em
  // data-metas-p e entra como variável CSS via WAAPI (cbRuntimeMotion).
  function initProgressChips() {
    var motion = window.cbRuntimeMotion;
    document.querySelectorAll('.metas-progress-wrap[data-metas-p]:not([data-p-ready])').forEach(function(wrap) {
      wrap.dataset.pReady = 'true';
      var p = parseInt(wrap.dataset.metasP, 10);
      if (isNaN(p)) p = 0;
      p = Math.max(0, Math.min(100, p));
      if (motion && typeof motion.hold === 'function') {
        motion.hold(wrap, { '--metas-p': String(p) });
      }
    });
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }

  document.body.addEventListener('htmx:afterSettle', function(e) {
    init();
    initProgressChips();
    handleAfterSettle(e);
    restorePendingTabFocus();
  });

  initProgressChips();
})();
