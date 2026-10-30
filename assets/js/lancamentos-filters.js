function toggleLancamentosSearch() {
    var el = document.getElementById('lancamentosSearch');
    if (el) el.classList.toggle('hidden');
}

function syncLancamentosFilterTrigger() {
    var panel = document.getElementById('lancamentosFilterPanel');
    var trigger = document.getElementById('lancamentosFilterTrigger');
    if (!panel || !trigger) return;
    var isHidden = panel.classList.contains('hidden');
    var wrapper = document.getElementById('lancamentos-list-wrapper');
    var active = wrapper
        ? wrapper.getAttribute('data-has-active-filters') === 'true'
        : trigger.getAttribute('data-has-active-filters') === 'true';
    trigger.setAttribute('aria-expanded', isHidden ? 'false' : 'true');
    trigger.setAttribute('aria-label', (isHidden ? 'Abrir filtros' : 'Fechar filtros') + (active ? ', filtros ativos' : ''));
    trigger.setAttribute('data-has-active-filters', active ? 'true' : 'false');
    trigger.classList.toggle('is-active', active);
}

var lastLancamentosFilterFocus = null;

function focusLancamentosFilterPanel() {
    var panel = document.getElementById('lancamentosFilterPanel');
    if (!panel) return;
    var close = panel.querySelector('[data-onclick="toggleLancamentosFilters"]');
    if (close && typeof close.focus === 'function') {
        close.focus({ preventScroll: true });
    }
}

function returnLancamentosFilterFocus() {
    var panel = document.getElementById('lancamentosFilterPanel');
    var active = document.activeElement;
    if (active && active !== document.body && panel && !panel.contains(active)) return;
    var trigger = document.getElementById('lancamentosFilterTrigger');
    if (!trigger || typeof trigger.focus !== 'function') return;
    if (lastLancamentosFilterFocus && lastLancamentosFilterFocus !== trigger && lastLancamentosFilterFocus !== document.body) {
        if (lastLancamentosFilterFocus.isConnected) {
            lastLancamentosFilterFocus.focus({ preventScroll: true });
            return;
        }
    }
    trigger.focus({ preventScroll: true });
}

function getLancamentosFilterFocusable() {
    var panel = document.getElementById('lancamentosFilterPanel');
    if (!panel) return [];
    return Array.prototype.slice.call(panel.querySelectorAll(
        'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), summary, [tabindex]:not([tabindex="-1"])'
    )).filter(function(el) {
        return !!(el.getClientRects().length && getComputedStyle(el).visibility !== 'hidden');
    });
}

function trapLancamentosFilterFocus(event) {
    if (event.key !== 'Tab') return;
    var panel = document.getElementById('lancamentosFilterPanel');
    if (!panel || panel.classList.contains('hidden')) return;
    if (typeof window.getTopOverlayId === 'function' && window.getTopOverlayId() !== 'lancamentosFilterPanel') return;
    var focusable = getLancamentosFilterFocusable();
    if (!focusable.length) return;
    var first = focusable[0];
    var last = focusable[focusable.length - 1];
    if (event.shiftKey && (document.activeElement === first || !panel.contains(document.activeElement))) {
        event.preventDefault();
        last.focus();
    } else if (!event.shiftKey && (document.activeElement === last || !panel.contains(document.activeElement))) {
        event.preventDefault();
        first.focus();
    }
}

function bindLancamentosFilterTriggerState() {
    var panel = document.getElementById('lancamentosFilterPanel');
    if (!panel || panel.dataset.triggerStateBound === 'true') return;
    panel.dataset.triggerStateBound = 'true';
    panel.dataset.filterWasOpen = panel.classList.contains('hidden') ? 'false' : 'true';
    if (typeof MutationObserver === 'function') {
        new MutationObserver(function() {
            var isHidden = panel.classList.contains('hidden');
            syncLancamentosFilterTrigger();
            var wasOpen = panel.dataset.filterWasOpen === 'true';
            if (isHidden && wasOpen) {
                returnLancamentosFilterFocus();
            }
            panel.dataset.filterWasOpen = isHidden ? 'false' : 'true';
        }).observe(panel, { attributes: true, attributeFilter: ['class'] });
    }
    syncLancamentosFilterTrigger();
}

function toggleLancamentosFilters() {
    var el = document.getElementById('lancamentosFilterPanel');
    if (el) {
        var wasHidden = el.classList.contains('hidden');
        if (wasHidden) lastLancamentosFilterFocus = document.activeElement;
        el.classList.toggle('hidden');
        syncLancamentosFilterTrigger();
        if (wasHidden && !el.classList.contains('hidden')) {
            if (typeof window.overlayDidOpen === 'function') window.overlayDidOpen('lancamentos-filters');
            window.setTimeout(focusLancamentosFilterPanel, 0);
        } else if (!wasHidden && el.classList.contains('hidden')) {
            if (typeof window.overlayDidClose === 'function') window.overlayDidClose();
            returnLancamentosFilterFocus();
        }
    }
}

function closeLancamentosFilters(event) {
    if (event) event.stopPropagation();
    var el = document.getElementById('lancamentosFilterPanel');
    if (el && !el.classList.contains('hidden')) {
        el.classList.add('hidden');
        syncLancamentosFilterTrigger();
        if (typeof window.overlayDidClose === 'function') window.overlayDidClose();
    }
}

function resetLancamentosFilters(mes, ano) {
    closeLancamentosFilters();
    var fallbackURL = '/lancamentos?mes=' + encodeURIComponent(mes || '') + '&ano=' + encodeURIComponent(ano || '');
    var headerReset = document.getElementById('clearFiltersBtn');
    var resetURL = headerReset && headerReset.getAttribute('data-clear-filters-url');
    if (!resetURL) resetURL = fallbackURL;
    if (window.htmx) {
        var focusAfterReset = function() {
            document.removeEventListener('htmx:afterSettle', focusAfterReset);
            var trigger = document.getElementById('lancamentosFilterTrigger');
            if (trigger) trigger.focus({ preventScroll: true });
        };
        document.addEventListener('htmx:afterSettle', focusAfterReset);
        htmx.ajax('GET', resetURL, {
            target: '#main-content',
            select: '#main-content',
            swap: 'outerHTML'
        });
        history.replaceState({htmx: true}, '', resetURL);
        return;
    }
    window.location.href = resetURL;
}

function syncLancamentosSearchFromURL() {
    var input = document.querySelector('#lancamentosFilters input[name="q"]');
    if (!input) return;
    var value = '';
    try {
        value = new URLSearchParams(window.location.search).get('q') || '';
    } catch (error) {
        return;
    }
    if (input.value !== value) input.value = value;
}

function syncLancamentosFilterChips(form) {
    if (!form) return;
    form.querySelectorAll('[data-filter-chip]').forEach(function(chip) {
        var input = chip.querySelector('input[type="checkbox"]');
        if (!input) return;
        var isChecked = input.checked;
        [
          'border-[var(--cb-border-strong)]','hover:border-[var(--cb-border-strong)]',
          'dark:border-amber-400/50','dark:bg-amber-500/20','dark:text-amber-200',
          'dark:border-red-400/50','dark:bg-red-500/20','dark:text-red-300',
          'dark:border-emerald-400/50','dark:bg-emerald-500/20','dark:text-emerald-200',
          'dark:border-blue-400/40','dark:bg-blue-500/20','dark:text-blue-200',
          'dark:border-violet-400/50','dark:bg-violet-500/15','dark:text-violet-200',
          'border-emerald-600/30','bg-emerald-600/10',
          'border-red-500/30','bg-red-500/10',
          'border-[#6744F1]/30','bg-[#6744F1]/10','text-[#6744F1]',
          'border-[#009866]/30','bg-[#009866]/10','text-[#009866]',
          'border-[#FE414F]/30','bg-[#FE414F]/10','text-[#FE414F]',
          'border-violet-300','bg-violet-50','text-violet-700','border-violet-500/25','dark:bg-violet-500/15','dark:text-violet-100',
          'border-emerald-300','bg-emerald-50','text-emerald-700','dark:border-emerald-500/25','dark:bg-emerald-500/15','dark:text-emerald-100',
          'border-red-300','bg-red-50','text-red-700','dark:border-red-500/25','dark:bg-red-500/15','dark:text-red-100',
          'border-blue-300','bg-blue-50','text-blue-700','dark:border-blue-500/25','dark:bg-blue-500/15','dark:text-blue-100',
          'border-amber-300','bg-amber-50','text-amber-700','dark:border-amber-500/25','dark:bg-amber-500/15','dark:text-amber-100',
          'border-zinc-200','bg-white','text-zinc-500','hover:border-zinc-300',
          'dark:border-zinc-700/40','dark:bg-zinc-800/60','dark:text-zinc-500','dark:hover:border-zinc-600/60',
          'border-zinc-200/50','bg-white/40','text-zinc-555','dark:border-zinc-800/60','dark:bg-zinc-900/30','dark:text-zinc-400','dark:hover:border-zinc-700/60',
          'border-zinc-200','bg-zinc-50/50','text-zinc-600','hover:border-zinc-300','dark:border-zinc-800/80','dark:bg-zinc-900/30','dark:text-zinc-400','dark:hover:border-zinc-700',
          'bg-zinc-50/50','text-zinc-600','dark:border-zinc-800/80','dark:hover:border-[#009866]/40','dark:hover:border-[#FE414F]/40','dark:hover:border-zinc-700','text-zinc-550'
        ].forEach(function(cls) { chip.classList.remove(cls); });
        if (isChecked) {
            var colorMap = {
                'receita':       ['border-emerald-600/30','bg-emerald-600/10','text-emerald-600','dark:border-emerald-400/50','dark:bg-emerald-500/20','dark:text-emerald-200'],
                'despesa':       ['border-red-500/30','bg-red-500/10','text-red-500','dark:border-red-400/50','dark:bg-red-500/20','dark:text-red-300'],
                'transferencia': ['border-blue-300','bg-blue-50','text-blue-700','dark:border-blue-400/40','dark:bg-blue-500/20','dark:text-blue-200'],
                'pendente':      ['border-amber-300','bg-amber-50','text-amber-700','dark:border-amber-400/50','dark:bg-amber-500/20','dark:text-amber-200'],
                'vencido':       ['border-red-500/30','bg-red-500/10','text-red-500','dark:border-red-400/50','dark:bg-red-500/20','dark:text-red-300'],
                'pago':          ['border-emerald-600/30','bg-emerald-600/10','text-emerald-600','dark:border-emerald-400/50','dark:bg-emerald-500/20','dark:text-emerald-200']
            };
            var classes = colorMap[input.value] || ['border-violet-500','bg-violet-500/10','text-violet-700','dark:border-violet-400/50','dark:bg-violet-500/15','dark:text-violet-200'];
            classes.forEach(function(cls) { chip.classList.add(cls); });
        } else {
            ['border-[var(--cb-border-strong)]','bg-[var(--cb-surface-1)]','text-[var(--cb-text-muted)]',
              'hover:border-[var(--cb-border-strong)]'].forEach(function(cls) { chip.classList.add(cls); });
        }
    });
}

function syncOrdemVisuals(form) {
    if (!form) return;
    var input = form.querySelector('input[name="ordem"]:checked');
    if (!input) return;
    var value = input.value;

    var ascLabel = document.getElementById('ordem-asc-label');
    var descLabel = document.getElementById('ordem-desc-label');
    if (!ascLabel || !descLabel) return;

    var allCls = [
        'border-violet-300', 'bg-violet-50', 'text-violet-750', 'dark:border-violet-500/25', 'dark:bg-violet-500/15', 'dark:text-violet-100',
        'dark:border-violet-400/50', 'dark:bg-violet-500/20', 'dark:text-violet-200',
        'border-zinc-200', 'bg-white', 'text-zinc-550', 'hover:border-zinc-300', 'hover:bg-zinc-50/50', 'dark:border-zinc-800/40', 'dark:bg-zinc-900/40', 'dark:text-zinc-400', 'dark:hover:border-zinc-700', 'hover:border-zinc-350'
    ];

    allCls.forEach(function(c) {
        ascLabel.classList.remove(c);
        descLabel.classList.remove(c);
    });

    var activeClasses = ['border-violet-300', 'bg-violet-50', 'text-violet-750', 'dark:border-violet-400/50', 'dark:bg-violet-500/20', 'dark:text-violet-200'];
    var inactiveClasses = ['border-zinc-200', 'bg-white', 'text-zinc-550', 'hover:border-zinc-350', 'hover:bg-zinc-50/50', 'dark:border-zinc-800/40', 'dark:bg-zinc-900/40', 'dark:text-zinc-400', 'dark:hover:border-zinc-700'];

    if (value === 'asc') {
        activeClasses.forEach(function(c) { ascLabel.classList.add(c); });
        inactiveClasses.forEach(function(c) { descLabel.classList.add(c); });
    } else {
        activeClasses.forEach(function(c) { descLabel.classList.add(c); });
        inactiveClasses.forEach(function(c) { ascLabel.classList.add(c); });
    }
}

document.addEventListener('change', function(e) {
    var form = e.target.closest('#lancamentosFilters');
    if (!form) return;
    // Close Mais panel when chip filter is clicked (only if clicked outside the panel itself).
    var panel = document.getElementById('lancamentosFilterPanel');
    if (panel && !panel.classList.contains('hidden') && !e.target.closest('#lancamentosFilterPanel')) {
        panel.classList.add('hidden');
        syncLancamentosFilterTrigger();
        if (typeof window.overlayDidClose === 'function') window.overlayDidClose();
    }
    // Reset bulk delete state before filter request.
    if (typeof window.resetLancamentosBulkState === 'function') {
        window.resetLancamentosBulkState();
    }
    syncLancamentosFilterChips(form);
    if (e.target.name === 'ordem') {
        syncOrdemVisuals(form);
    }
});

document.addEventListener('htmx:afterSwap', function(e) {
    var form = document.getElementById('lancamentosFilters');
    if (form) {
        syncLancamentosFilterChips(form);
        updateLancamentosCategoryVisuals(form);
        syncOrdemVisuals(form);
    }
});

function initLancamentosFilterForm() {
    var form = document.getElementById('lancamentosFilters');
    if (!form) return;
    bindLancamentosFilterTriggerState();
    syncLancamentosFilterChips(form);
    updateLancamentosCategoryVisuals(form);
    syncOrdemVisuals(form);
}

document.addEventListener('submit', function(e) {
    if (e.target && e.target.id === 'lancamentosFilters') e.preventDefault();
});

initLancamentosFilterForm();

window.selectLancamentosFilterCategory = function(id, name) {
    var form = document.getElementById('lancamentosFilters');
    if (!form) return;
    // Close Mais panel when a category is selected.
    var panel = document.getElementById('lancamentosFilterPanel');
    if (panel && !panel.classList.contains('hidden')) {
        panel.classList.add('hidden');
        syncLancamentosFilterTrigger();
        if (typeof window.overlayDidClose === 'function') window.overlayDidClose();
    }
    // Reset bulk delete state before category filter request.
    if (typeof window.resetLancamentosBulkState === 'function') {
        window.resetLancamentosBulkState();
    }
    var existing = form.querySelectorAll('input[name="categoria"]');
    var isDeselect = false;
    existing.forEach(function(el) {
        if (el.value === id) isDeselect = true;
        el.remove();
    });
    if (!isDeselect) {
        var input = document.createElement('input');
        input.type = 'hidden';
        input.name = 'categoria';
        input.value = id;
        form.appendChild(input);
    }
    updateLancamentosCategoryVisuals(form);
    htmx.trigger(form, 'change');
};

function updateLancamentosCategoryVisuals(form) {
    var selected = [];
    if (form) {
        form.querySelectorAll('input[name="categoria"]').forEach(function(el) {
            selected.push(el.value);
        });
    }
    var items = document.querySelectorAll('.lancamentos-category-item');
    items.forEach(function(item) {
        var id = item.getAttribute('data-category-id');
        var isSelected = selected.indexOf(id) !== -1;
        item.classList.remove('bg-[#6744F1]/10','text-[#6744F1]','dark:bg-[#6744F1]/20','dark:text-violet-300');
        item.classList.remove('text-zinc-600','hover:bg-zinc-100','dark:text-zinc-300','dark:hover:bg-zinc-800/60');
        if (isSelected) {
            item.classList.add('bg-[#6744F1]/10','text-[#6744F1]','dark:bg-[#6744F1]/20','dark:text-violet-300');
        } else {
            item.classList.add('text-zinc-600','hover:bg-zinc-100','dark:text-zinc-300','dark:hover:bg-zinc-800/60');
        }
    });
}

function filterLancamentosCategories(query) {
    var q = normalizeSearchText(query || '');
    var items = document.querySelectorAll('.lancamentos-category-item');
    var clearBtn = document.getElementById('lancamentosCategorySearchClear');
    if (clearBtn) {
        if (q) {
            clearBtn.classList.remove('hidden');
            clearBtn.classList.add('inline-flex');
        } else {
            clearBtn.classList.add('hidden');
            clearBtn.classList.remove('inline-flex');
        }
    }
    items.forEach(function(item) {
        var name = normalizeSearchText(item.getAttribute('data-category-name') || '');
        var type = normalizeSearchText(item.getAttribute('data-category-type') || '');
        item.classList.toggle('hidden', q && name.indexOf(q) === -1 && type.indexOf(q) === -1);
    });
}

// CSP-safe wrappers for data-* event delegation (Fase C1)
window.filterLancamentosCategoriesInput = function(el) {
    filterLancamentosCategories(el.value);
};

window.handleLancamentosCategorySelect = function(event) {
    var btn = event.target.closest('[data-category-id]');
    if (!btn) return;
    selectLancamentosFilterCategory(btn.getAttribute('data-category-id'), btn.getAttribute('data-category-name'));
};

window.resetLancamentosFiltersFromForm = function() {
    var form = document.getElementById('lancamentosFilters');
    var mes = form ? (form.querySelector('[name="mes"]') ? form.querySelector('[name="mes"]').value : '') : '';
    var ano = form ? (form.querySelector('[name="ano"]') ? form.querySelector('[name="ano"]').value : '') : '';
    resetLancamentosFilters(mes, ano);
};

window.handleClearLancamentosFilters = function(event) {
    if (event && typeof event.preventDefault === 'function') event.preventDefault();
    window.resetLancamentosFiltersFromForm();
};

document.addEventListener('keydown', function(event) {
    var panel = document.getElementById('lancamentosFilterPanel');
    if (!panel || panel.classList.contains('hidden')) return;
    if (typeof window.getTopOverlayId === 'function' && window.getTopOverlayId() !== 'lancamentosFilterPanel') return;
    if (event.key === 'Escape') {
        event.preventDefault();
        closeLancamentosFilters();
        return;
    }
    trapLancamentosFilterFocus(event);
});

document.addEventListener('htmx:afterSettle', function() {
    initLancamentosFilterForm();
    syncLancamentosFilterTrigger();
});

document.addEventListener('htmx:historyRestore', function() {
    syncLancamentosFilterTrigger();
    syncLancamentosSearchFromURL();
});

window.addEventListener('popstate', function() {
    window.setTimeout(syncLancamentosSearchFromURL, 0);
});

syncLancamentosSearchFromURL();
