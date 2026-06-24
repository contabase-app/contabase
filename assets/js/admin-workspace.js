(function() {
  'use strict';

  var CARD_EDIT_CLASSES = 'ring-1 ring-violet-400/30 border-violet-400/30 dark:ring-violet-500/30 dark:border-violet-500/35';

  function clearBusinessFields(scope) {
    var names = ['company_name', 'cnpj_cpf', 'address', 'phone'];
    names.forEach(function(name) {
      var el = scope.querySelector('[name="' + name + '"]');
      if (el) el.value = '';
    });
  }

  function toggleBusinessSections(scope, type) {
    var sections = scope.querySelectorAll('[data-admin-ws-business]');
    sections.forEach(function(s) {
      s.classList.toggle('hidden', type !== 'business');
    });
    if (type !== 'business') {
      clearBusinessFields(scope);
    }
  }

  function initCreateForm(form) {
    if (form.dataset.wsCreateReady === 'true') return;
    form.dataset.wsCreateReady = 'true';

    var typeSelect = form.querySelector('[data-admin-ws-type]');
    if (!typeSelect) return;

    typeSelect.addEventListener('change', function() {
      toggleBusinessSections(form, typeSelect.value);
    });
  }

  function initCard(card) {
    if (card.dataset.wsCardReady === 'true') return;
    card.dataset.wsCardReady = 'true';

    var view = card.querySelector('[data-admin-ws-view]');
    var form = card.querySelector('[data-admin-ws-form]');
    var toggle = card.querySelector('[data-admin-ws-toggle]');
    var toggleText = card.querySelector('[data-admin-ws-toggle-text]');

    if (!view || !form) return;

    function showEdit() {
      view.classList.add('hidden');
      form.classList.remove('hidden');
      card.classList.add.apply(card.classList, CARD_EDIT_CLASSES.split(' '));
      if (toggleText) toggleText.textContent = 'Cancelar';
    }

    function showView() {
      view.classList.remove('hidden');
      form.classList.add('hidden');
      card.classList.remove.apply(card.classList, CARD_EDIT_CLASSES.split(' '));
      if (toggleText) toggleText.textContent = 'Editar';
    }

    if (toggle) {
      toggle.addEventListener('click', function() {
        if (form.classList.contains('hidden')) {
          showEdit();
        } else {
          showView();
        }
      });
    }

    var typeSelect = form.querySelector('[data-admin-ws-type]');
    if (typeSelect) {
      typeSelect.addEventListener('change', function() {
        toggleBusinessSections(form, typeSelect.value);
      });
    }
  }

  function init(root) {
    root = root || document;

    var createForm = root.querySelector('[data-admin-ws-create]');
    if (createForm) initCreateForm(createForm);

    root.querySelectorAll('[data-admin-ws-card]').forEach(initCard);
  }

  document.addEventListener('input', function(e) {
    var el = e.target;
    if (el.dataset.adminMask === 'cnpj' && typeof window.maskCnpjCpf === 'function') {
      window.maskCnpjCpf(el);
    } else if (el.dataset.adminMask === 'phone' && typeof window.maskPhone === 'function') {
      window.maskPhone(el);
    }
  });

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function() { init(document); });
  } else {
    init(document);
  }

  document.body.addEventListener('htmx:afterSwap', function() { init(document); });
})();
