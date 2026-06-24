(function() {
  'use strict';

  function state(container) {
    if (!container._catState) {
      container._catState = {
        currentTab: container.dataset.catDefaultTab || 'EXPENSE',
        selectMacro: container.dataset.catDefaultMacro || '',
        parentId: '',
        parentSearch: '',
        open: false
      };
    }
    return container._catState;
  }

  function comboboxElements(container) {
    var cb = container.querySelector('[data-cat-combobox]');
    if (!cb) return null;
    return {
      container: container,
      root: cb,
      toggle: cb.querySelector('[data-cat-combobox-toggle]'),
      label: cb.querySelector('[data-cat-combobox-label]'),
      panel: cb.querySelector('[data-cat-combobox-panel]'),
      search: cb.querySelector('[data-cat-combobox-search]'),
      hidden: cb.querySelector('[data-cat-combobox-hidden]'),
      options: cb.querySelectorAll('[data-cat-combobox-option]')
    };
  }

  function openCombobox(cb) {
    var st = state(cb.container);
    if (st.open) return;
    st.open = true;
    var panel = cb.panel;
    if (!panel) return;
    panel.classList.remove('hidden');
    requestAnimationFrame(function() {
      panel.classList.remove('opacity-0', 'scale-95');
      panel.classList.add('opacity-100', 'scale-100');
    });
    updateComboboxFilter(cb);
    if (cb.search) {
      setTimeout(function() { cb.search.focus(); cb.search.select(); }, 50);
    }
  }

  function closeCombobox(cb) {
    var st = state(cb.container);
    if (!st.open) return;
    st.open = false;
    var panel = cb.panel;
    if (!panel) return;
    panel.classList.remove('opacity-100', 'scale-100');
    panel.classList.add('opacity-0', 'scale-95');
    setTimeout(function() {
      if (!state(cb.container).open) panel.classList.add('hidden');
    }, 150);
  }

  function closeAllComboboxes(root) {
    root.querySelectorAll('[data-cat-root][data-cat-ready]').forEach(function(container) {
      var cb = comboboxElements(container);
      if (cb) closeCombobox(cb);
    });
  }

  function updateComboboxFilter(cb) {
    var st = state(cb.container);
    cb.options.forEach(function(opt) {
      var optType = opt.dataset.catOptionType;
      var optMacro = opt.dataset.catOptionMacro || '';
      var optName = (opt.dataset.catOptionName || '').toLowerCase();
      var optValue = opt.dataset.catOptionValue;

      var typeMatch = !optType || optType === st.currentTab;
      var searchMatch = !st.parentSearch || optName.indexOf(st.parentSearch.toLowerCase()) !== -1 || optValue === '';

      opt.classList.toggle('hidden', !(typeMatch && searchMatch));

      var isSelected = optValue === st.parentId;
      if (isSelected) {
        opt.dataset.catOptionSelected = 'true';
      } else {
        delete opt.dataset.catOptionSelected;
      }

      var activeCls = (opt.dataset.catOptionActive || '').split(' ').filter(Boolean);
      var inactiveCls = (opt.dataset.catOptionInactive || '').split(' ').filter(Boolean);
      activeCls.forEach(function(c) { opt.classList.toggle(c, isSelected); });
      inactiveCls.forEach(function(c) { opt.classList.toggle(c, !isSelected); });
    });
  }

  function updateComboboxLabel(cb) {
    var st = state(cb.container);
    if (!cb.label) return;
    var sel = cb.root.querySelector('[data-cat-combobox-option][data-cat-option-selected]');
    var name = sel ? sel.dataset.catOptionName : 'Nenhuma (categoria raiz)';
    cb.label.textContent = name || 'Nenhuma (categoria raiz)';
  }

  function selectParent(cb, id, name) {
    var st = state(cb.container);
    st.parentId = id;
    st.parentSearch = '';
    closeCombobox(cb);
    updateComboboxLabel(cb);
    updateComboboxFilter(cb);
    updateConditionalSections(cb.container);
    if (cb.hidden) cb.hidden.value = st.parentId;
    if (cb.search) cb.search.value = '';
  }

  function getDefaultMacroForType(container, typ) {
    var isBusiness = container.dataset.catIsBusiness === 'true';
    if (isBusiness) {
      return typ === 'INCOME' ? 'Receitas Operacionais' : 'Custos Operacionais';
    }
    return typ === 'INCOME' ? 'Receitas' : 'Essencial';
  }

  function updateMacroFilter(container) {
    var st = state(container);
    var macroSel = container.querySelector('[data-cat-macro-select]');
    if (!macroSel) return;

    var currentType = st.currentTab;
    Array.from(macroSel.options).forEach(function(opt) {
      var forTypes = (opt.dataset.catMacroForType || '').split(',').map(function(s) { return s.trim(); });
      if (forTypes.indexOf(currentType) >= 0) {
        opt.style.display = '';
      } else {
        opt.style.display = 'none';
      }
    });

    var visibleValues = Array.from(macroSel.options)
      .filter(function(o) { return o.style.display !== 'none'; })
      .map(function(o) { return o.value; });

    if (visibleValues.indexOf(st.selectMacro) < 0 && visibleValues.indexOf('') >= 0) {
      st.selectMacro = getDefaultMacroForType(container, currentType);
    }
    if (st.selectMacro && visibleValues.indexOf(st.selectMacro) >= 0) {
      macroSel.value = st.selectMacro;
    } else {
      macroSel.value = '';
      st.selectMacro = '';
    }
  }

  function updateTabs(container) {
    var st = state(container);
    container.querySelectorAll('[data-cat-tab]').forEach(function(tab) {
      var icon = tab.querySelector('[data-cat-tab-icon]');
      var tv = tab.dataset.catTab;
      var isActive = tv === st.currentTab;

      var activeCls = (tab.dataset.catTabActive || '').split(' ').filter(Boolean);
      var inactiveCls = (tab.dataset.catTabInactive || '').split(' ').filter(Boolean);
      var iconActive = icon ? (tab.dataset.catTabIconActive || '').split(' ').filter(Boolean) : [];
      var iconInactive = icon ? (tab.dataset.catTabIconInactive || '').split(' ').filter(Boolean) : [];

      activeCls.forEach(function(c) { tab.classList.toggle(c, isActive); });
      inactiveCls.forEach(function(c) { tab.classList.toggle(c, !isActive); });
      if (icon) {
        iconActive.forEach(function(c) { icon.classList.toggle(c, isActive); });
        iconInactive.forEach(function(c) { icon.classList.toggle(c, !isActive); });
      }
    });

    container.querySelectorAll('[data-cat-tab-panel]').forEach(function(p) {
      p.classList.toggle('hidden', p.dataset.catTabPanel !== st.currentTab);
    });

    var typeSel = container.querySelector('[data-cat-type-select]');
    if (typeSel) typeSel.value = st.currentTab;

    updateMacroFilter(container);
  }

  function updateConditionalSections(container) {
    var st = state(container);
    var isRoot = st.parentId === '';

    var macroSection = container.querySelector('[data-cat-macro-section]');
    var subInfo = container.querySelector('[data-cat-sub-info]');
    var macroCustom = container.querySelector('[data-cat-macro-custom]');

    if (macroSection) macroSection.classList.toggle('hidden', !isRoot);
    if (subInfo) subInfo.classList.toggle('hidden', isRoot);
    if (macroCustom) macroCustom.classList.toggle('hidden', st.selectMacro !== 'custom');
  }

  function setParentCollapsed(block, collapsed) {
    if (!block) return;
    var children = block.querySelector('[data-cat-children]');
    var chevron = block.querySelector('[data-cat-chevron]');
    var toggleBtn = block.querySelector('[data-cat-toggle]');
    if (!children || !toggleBtn) return;

    if (collapsed) {
      children.classList.add('hidden');
      if (chevron) chevron.style.transform = 'rotate(-90deg)';
      toggleBtn.setAttribute('aria-label', 'Expandir subcategorias');
      toggleBtn.setAttribute('aria-expanded', 'false');
    } else {
      children.classList.remove('hidden');
      if (chevron) chevron.style.transform = '';
      toggleBtn.setAttribute('aria-label', 'Ocultar subcategorias');
      toggleBtn.setAttribute('aria-expanded', 'true');
    }
  }

  function collapseAllParents(container) {
    container.querySelectorAll('[data-cat-parent-block]').forEach(function(block) {
      setParentCollapsed(block, true);
    });
  }

  function toggleParentCollapse(toggleBtn) {
    var block = toggleBtn.closest('[data-cat-parent-block]');
    if (!block) return;
    var children = block.querySelector('[data-cat-children]');
    if (!children) return;

    var isOpen = !children.classList.contains('hidden');
    setParentCollapsed(block, isOpen);
  }

  function filterCategoriesBySearch(container, query) {
    query = (query || '').toLowerCase().trim();
    var parentBlocks = container.querySelectorAll('[data-cat-parent-block]');

    if (!query) {
      container.querySelectorAll('[data-cat-children] article').forEach(function(article) {
        article.classList.remove('hidden');
      });
      collapseAllParents(container);
      parentBlocks.forEach(function(block) {
        block.classList.remove('hidden');
      });
      return;
    }

    collapseAllParents(container);

    parentBlocks.forEach(function(block) {
      var parentName = '';
      var nameEl = block.querySelector('[data-cat-parent-header] [data-cat-name]');
      if (nameEl) parentName = (nameEl.textContent || '').toLowerCase();

      var childrenContainer = block.querySelector('[data-cat-children]');
      var childRows = childrenContainer ? childrenContainer.querySelectorAll('[data-cat-name]') : [];

      var parentMatch = !query || parentName.indexOf(query) !== -1;
      var anyChildMatch = false;
      childRows.forEach(function(row) {
        var childName = (row.textContent || '').toLowerCase();
        var match = !query || childName.indexOf(query) !== -1;
        var article = row.closest('article');
        if (article) article.classList.toggle('hidden', !match);
        if (match) anyChildMatch = true;
      });

      if (parentMatch || anyChildMatch) {
        block.classList.remove('hidden');
        if (anyChildMatch) setParentCollapsed(block, false);
      } else {
        block.classList.add('hidden');
      }
    });
  }

  function init(scope) {
    scope = scope || document;
    scope.querySelectorAll('[data-cat-root]:not([data-cat-ready])').forEach(function(container) {
      container.dataset.catReady = 'true';

      var cb = comboboxElements(container);

      // Tabs
      container.querySelectorAll('[data-cat-tab]').forEach(function(tab) {
        tab.addEventListener('click', function() {
          var st = state(container);
          var newTab = tab.dataset.catTab;
          if (newTab && newTab !== st.currentTab) {
            st.currentTab = newTab;
            updateTabs(container);
            if (cb) updateComboboxFilter(cb);
          }
        });
      });

      // Type select sync
      var typeSel = container.querySelector('[data-cat-type-select]');
      if (typeSel) {
        typeSel.addEventListener('change', function() {
          var st = state(container);
          st.currentTab = typeSel.value;
          st.selectMacro = getDefaultMacroForType(container, st.currentTab);
          selectParent(cb, '', '');
          updateTabs(container);
          if (cb) updateComboboxFilter(cb);
        });
      }

      // Combobox toggle
      if (cb && cb.toggle) {
        cb.toggle.addEventListener('click', function(e) {
          e.preventDefault();
          var st = state(container);
          if (st.open) closeCombobox(cb); else openCombobox(cb);
        });
      }

      // Combobox search
      if (cb && cb.search) {
        cb.search.addEventListener('input', function() {
          state(container).parentSearch = cb.search.value;
          updateComboboxFilter(cb);
        });
        cb.search.addEventListener('keydown', function(e) {
          if (e.key === 'Escape') {
            closeCombobox(cb);
            if (cb.toggle) cb.toggle.focus();
          }
        });
      }

      // Combobox option clicks
      if (cb) {
        cb.options.forEach(function(opt) {
          opt.addEventListener('click', function() {
            selectParent(cb, opt.dataset.catOptionValue, opt.dataset.catOptionName);
          });
        });
      }

      // Macro select
      var macroSel = container.querySelector('[data-cat-macro-select]');
      if (macroSel) {
        var st = state(container);
        if (st.selectMacro) macroSel.value = st.selectMacro;

        macroSel.addEventListener('change', function() {
          st.selectMacro = macroSel.value;
          var macroCustom = container.querySelector('[data-cat-macro-custom]');
          if (macroCustom) macroCustom.classList.toggle('hidden', st.selectMacro !== 'custom');
          selectParent(cb, '', '');
          if (cb) updateComboboxFilter(cb);
        });
      }

      // Init combobox panel transition classes
      if (cb && cb.panel) {
        cb.panel.classList.add('transition-all', 'duration-150');
      }

      // Parent collapse toggles
      container.querySelectorAll('[data-cat-toggle]').forEach(function(toggle) {
        toggle.addEventListener('click', function(e) {
          e.preventDefault();
          toggleParentCollapse(toggle);
        });
      });

      // Search filter
      var searchInput = container.querySelector('[data-cat-search]');
      if (searchInput) {
        searchInput.addEventListener('input', function() {
          filterCategoriesBySearch(container, searchInput.value);
        });
      }

      // Init state
      if (searchInput && searchInput.value) {
        filterCategoriesBySearch(container, searchInput.value);
      } else {
        collapseAllParents(container);
      }
      updateTabs(container);
      updateConditionalSections(container);
      if (cb) updateComboboxFilter(cb);
    });
  }

  // Click outside closes comboboxes
  document.addEventListener('click', function(e) {
    var inside = e.target.closest('[data-cat-combobox]');
    if (inside) return;
    closeAllComboboxes(document);
  });

  // ESC closes comboboxes
  document.addEventListener('keydown', function(e) {
    if (e.key !== 'Escape') return;
    closeAllComboboxes(document);
  });

  // Lifecycle
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function() { init(document); });
  } else {
    init(document);
  }

  document.body.addEventListener('htmx:afterSwap', function() { init(document); });
})();
