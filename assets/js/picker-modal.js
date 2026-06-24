// Picker Modal — overlay registration + search keydown for modal_picker.html
// Phase I1: replaces inline <script> that called overlayDidOpen() + refreshIcons()
// Phase I3: replaces inline onkeydown on search input (Enter → dispatch 'search' event for HTMX)
// refreshIcons() is handled by htmx-lifecycle.js initAppShell on every htmx:afterSwap
(function() {
  'use strict';

  document.body.addEventListener('htmx:afterSwap', function(event) {
    if (typeof window.overlayDidOpen !== 'function') return;
    var target = event.detail && event.detail.target;
    if (!target) return;

    // Check if the swapped content contains the picker backdrop.
    // When the picker first opens into a container (e.g., #bottom-sheet-container),
    // target.querySelector finds #modalPickerBackdrop inside it and registers the overlay.
    // Subsequent searches swap into #picker-list-container, which is a child of the backdrop,
    // so querySelector won't find the backdrop there — no duplicate overlayDidOpen calls.
    if (
      target.id === 'modalPickerBackdrop' ||
      (target.querySelector && target.querySelector('#modalPickerBackdrop'))
    ) {
      window.overlayDidOpen('picker-modal:afterSwap');
    }
  });

  document.addEventListener('keydown', function(e) {
    if (e.key !== 'Enter') return;
    var input = e.target.closest('[data-picker-search]');
    if (!input) return;
    e.preventDefault();
    input.dispatchEvent(new Event('search'));
  });

  function syncMacroInheritState(form) {
    if (!form) return;
    var parentSelect = form.querySelector('select[name="parent_id"]');
    var macroSelect = form.querySelector('select[name="macro_group"]');
    var helpEl = form.querySelector('.macro-inherit-help');
    if (!parentSelect || !macroSelect) return;

    if (parentSelect.value) {
      var selectedOption = parentSelect.options[parentSelect.selectedIndex];
      var parentMacro = selectedOption ? selectedOption.getAttribute('data-macro-group') : '';
      if (parentMacro) {
        macroSelect.setAttribute('data-prev-macro', macroSelect.value);
        macroSelect.value = parentMacro;
        macroSelect.disabled = true;
      }
      if (helpEl) helpEl.classList.remove('hidden');
    } else {
      var prevMacro = macroSelect.getAttribute('data-prev-macro');
      if (prevMacro) {
        macroSelect.value = prevMacro;
        macroSelect.removeAttribute('data-prev-macro');
      }
      macroSelect.disabled = false;
      if (helpEl) helpEl.classList.add('hidden');
    }
  }

  document.body.addEventListener('change', function(e) {
    var parentSelect = e.target.closest('select[name="parent_id"]');
    if (!parentSelect) return;
    syncMacroInheritState(parentSelect.closest('form'));
  });

  document.body.addEventListener('htmx:afterSwap', function(e) {
    var target = e.detail && e.detail.target;
    if (!target) return;
    var parentSelect = target.querySelector('select[name="parent_id"]');
    if (parentSelect) {
      syncMacroInheritState(parentSelect.closest('form'));
    }
  });

  document.body.addEventListener('categoryCreated', function(e) {
    var data = e.detail;
    if (!data || !data.id || !data.name) return;

    var ns = window.ContaBaseFormLancamento;
    if (ns && ns.applyCategorySelection) {
      ns.applyCategorySelection(
        { id: data.id, name: data.name, icon: data.icon, color: data.color, type: data.type, boxId: '', boxReserved: '0' },
        {
          visualMode: 'htmx-picker',
          onBoxOverdraftChange: function() { if (window.updateBoxOverdraftWarning) window.updateBoxOverdraftWarning(); },
          onRefreshIcons: function() { if (typeof window.refreshIcons === 'function') window.refreshIcons(); }
        }
      );
    } else if (typeof window.selectPickerItem === 'function') {
      window.selectPickerItem(data.id, data.name, data.icon, data.color, data.type, '', '0');
    }

    var picker = document.getElementById('modalPickerBackdrop');
    if (picker) picker.remove();
    if (typeof window.overlayDidClose === 'function') window.overlayDidClose();
  });
})();
