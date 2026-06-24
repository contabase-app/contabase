(function() {
  'use strict';

  function closeCaixinha(dd) {
    dd.classList.remove('is-open');
    var panel = dd.querySelector('[data-ck-panel]');
    if (panel) panel.classList.add('hidden');
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
    setMode(dd, 'menu');
  }

  function init() {
    document.querySelectorAll('[data-caixinha-dropdown]:not([data-ck-ready])').forEach(function(dd) {
      dd.dataset.ckReady = 'true';

      var toggle = dd.querySelector('[data-ck-toggle]');
      var panel = dd.querySelector('[data-ck-panel]');
      if (!toggle || !panel) return;

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
        var modeBtn = e.target.closest('[data-ck-mode]');
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
  }

  document.addEventListener('click', function(e) {
    if (e.target.closest('[data-caixinha-dropdown]')) return;
    document.querySelectorAll('[data-caixinha-dropdown].is-open').forEach(function(dd) {
      closeCaixinha(dd);
    });
  });

  document.addEventListener('keydown', function(e) {
    if (e.key !== 'Escape') return;
    document.querySelectorAll('[data-caixinha-dropdown].is-open').forEach(function(dd) {
      closeCaixinha(dd);
    });
  });

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }

  document.body.addEventListener('htmx:afterSettle', function() {
    init();
  });
})();
