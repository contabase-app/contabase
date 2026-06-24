(function() {
  'use strict';

  function openDropdown(dd) {
    var panel = dd.querySelector('[data-dropdown-panel]');
    if (!panel) return;
    dd.classList.add('is-open');
    panel.classList.remove('hidden');
    var chevron = dd.querySelector('[data-dropdown-chevron]');
    if (chevron) chevron.classList.add('rotate-180');
    if (dd.hasAttribute('aria-expanded')) dd.setAttribute('aria-expanded', 'true');
  }

  function closeDropdown(dd) {
    var panel = dd.querySelector('[data-dropdown-panel]');
    dd.classList.remove('is-open');
    if (panel) panel.classList.add('hidden');
    var chevron = dd.querySelector('[data-dropdown-chevron]');
    if (chevron) chevron.classList.remove('rotate-180');
    if (dd.hasAttribute('aria-expanded')) dd.setAttribute('aria-expanded', 'false');
  }

  function closeAllDropdowns() {
    document.querySelectorAll('[data-dropdown].is-open').forEach(function(dd) {
      closeDropdown(dd);
    });
  }

  function toggleDropdown(dd) {
    if (dd.classList.contains('is-open')) {
      closeDropdown(dd);
    } else {
      closeAllDropdowns();
      openDropdown(dd);
    }
  }

  function init(root) {
    root = root || document;
    root.querySelectorAll('[data-dropdown]:not([data-dd-ready])').forEach(function(dd) {
      dd.dataset.ddReady = 'true';

      var toggle = dd.querySelector('[data-dropdown-toggle]');
      var panel = dd.querySelector('[data-dropdown-panel]');
      if (!toggle || !panel) return;

      toggle.addEventListener('click', function(e) {
        e.preventDefault();
        e.stopPropagation();
        toggleDropdown(dd);
      });

      dd.addEventListener('click', function(e) {
        var hxEl = e.target.closest('[hx-get], [hx-post], [hx-put], [hx-delete], [hx-patch]');
        if (hxEl) {
          if (dd.classList.contains('is-open')) {
            closeDropdown(dd);
          }
          return;
        }
        e.stopPropagation();
        if (dd.classList.contains('is-open')) {
          closeDropdown(dd);
        }
      });
    });
  }

  document.addEventListener('click', function(e) {
    if (e.target.closest('[data-dropdown]')) return;
    closeAllDropdowns();
  });

  document.addEventListener('keydown', function(e) {
    if (e.key !== 'Escape') return;
    var open = document.querySelector('[data-dropdown].is-open');
    if (open) {
      closeDropdown(open);
      var toggle = open.querySelector('[data-dropdown-toggle]');
      if (toggle) toggle.focus();
    }
  });

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function() { init(document); });
  } else {
    init(document);
  }

  document.body.addEventListener('htmx:afterSwap', function() {
    init(document);
    document.querySelectorAll('[data-dropdown].is-open').forEach(function(dd) {
      var panel = dd.querySelector('[data-dropdown-panel]');
      if (panel && panel.classList.contains('hidden')) {
        dd.classList.remove('is-open');
      }
    });
  });
})();
