// Form Meta — formulário de Limites de Gastos.
// CSP-safe external JS: sem scripts inline, usa data-* e delegação de eventos.
// A validação client-side cobre apenas obrigatoriedade e valor maior que zero;
// as regras financeiras continuam no backend.
(function() {
    'use strict';

    function moneyIsPositive(value) {
        var digits = String(value || '').replace(/\D/g, '');
        return digits !== '' && Number(digits) > 0;
    }

    var pickerTrigger = null;

    function installPickerFocusBridge() {
        if (window.__metasPickerFocusInstalled) return;
        if (typeof window.closePickerModal !== 'function') return;
        var originalClose = window.closePickerModal;
        window.closePickerModal = function() {
            var trigger = pickerTrigger;
            var result = originalClose.apply(this, arguments);
            pickerTrigger = null;
            if (trigger && typeof trigger.focus === 'function') trigger.focus();
            return result;
        };
        window.__metasPickerFocusInstalled = true;
    }

    function setMetaFieldError(id, message) {
        var el = document.getElementById(id);
        if (!el) return;
        el.textContent = message;
        el.classList.remove('hidden');
    }

    function clearMetaFieldError(id) {
        var el = document.getElementById(id);
        if (!el) return;
        el.textContent = '';
        el.classList.add('hidden');
    }

    function setMetaInvalid(el, invalid) {
        if (!el) return;
        el.setAttribute('aria-invalid', invalid ? 'true' : 'false');
    }

    function categoryPicker() {
        return document.querySelector('#categoriaPicker_limite [data-meta-category-picker]');
    }

    function clearCategoryState() {
        clearMetaFieldError('categoria-error-limite');
        setMetaInvalid(categoryPicker(), false);
    }

    function clearMetaValidationState() {
        clearCategoryState();
        clearMetaFieldError('limite-amount-error');
        setMetaInvalid(document.getElementById('limiteMaxAmount'), false);
    }

    function focusMetaFormError() {
        var error = document.querySelector('#limite-form-error:not(.hidden)');
        if (!error || error.dataset.metaErrorFocused === 'true') return;
        error.dataset.metaErrorFocused = 'true';
        if (!error.hasAttribute('tabindex')) error.setAttribute('tabindex', '-1');
        error.focus();
    }

    function validateMetaForm(form) {
        if (!form || form.id !== 'meta-limite-form') return true;
        var category = document.getElementById('categoriaId_limite');
        var picker = categoryPicker();
        var amount = document.getElementById('limiteMaxAmount');
        clearMetaFieldError('categoria-error-limite');
        clearMetaFieldError('limite-amount-error');
        setMetaInvalid(picker, false);
        setMetaInvalid(amount, false);

        if (!category || !String(category.value || '').trim()) {
            setMetaFieldError('categoria-error-limite', 'Selecione uma categoria.');
            setMetaInvalid(picker, true);
            if (picker && typeof picker.focus === 'function') picker.focus();
            return false;
        }
        if (!amount || !moneyIsPositive(amount.value)) {
            setMetaFieldError('limite-amount-error', 'Informe um valor maior que zero.');
            setMetaInvalid(amount, true);
            if (amount && typeof amount.focus === 'function') amount.focus();
            return false;
        }
        return true;
    }

    function initMetaSelectPickerItem() {
        window.selectPickerItem = function(id, name, icon, color) {
            var form = document.getElementById('meta-limite-form');
            // Only handle this picker when the limit form is in the DOM.
            if (!form) return;

            var catIdEl = document.getElementById('categoriaId_limite');
            if (!catIdEl) return;
            catIdEl.value = id;
            clearCategoryState();

            var nomeEl = document.getElementById('categoriaNome_limite');
            if (nomeEl) nomeEl.innerText = name;

            var iconEl = document.getElementById('categoriaIcon_limite');
            if (iconEl) {
                iconEl.setAttribute('data-lucide', icon);
                if (typeof window.applyRuntimeColor === 'function') window.applyRuntimeColor(iconEl, color);
            }

            var wrap = document.getElementById('categoriaIconWrap_limite');
            if (wrap && typeof window.applyRuntimeColor === 'function') window.applyRuntimeColor(wrap, color);

            pickerTrigger = null;
            var pickerButton = categoryPicker();
            if (pickerButton && typeof pickerButton.focus === 'function') pickerButton.focus();

            if (typeof window.refreshIcons === 'function') window.refreshIcons();
        };
    }

    function isMetaFormInDOM() {
        return !!document.querySelector('[data-meta-form-root]');
    }

    function initMetaForm() {
        if (!isMetaFormInDOM()) return;
        initMetaSelectPickerItem();
        clearMetaValidationState();
    }

    document.addEventListener('click', function(e) {
        var button = e.target.closest ? e.target.closest('[data-meta-category-picker]') : null;
        if (!button) return;
        pickerTrigger = button;
        installPickerFocusBridge();
    }, true);

    document.addEventListener('submit', function(e) {
        var form = e.target;
        if (!form || form.id !== 'meta-limite-form') return;
        if (validateMetaForm(form)) return;
        e.preventDefault();
        e.stopImmediatePropagation();
    }, true);

    document.addEventListener('input', function(e) {
        if (e.target.id === 'limiteMaxAmount') {
            clearMetaFieldError('limite-amount-error');
            setMetaInvalid(e.target, false);
        }
    });

    // Initialize when the meta form is swapped into the bottom sheet.
    document.body.addEventListener('htmx:afterSwap', function(e) {
        if (e.detail.target && (e.detail.target.id === 'bottom-sheet-container' || e.detail.target.querySelector('[data-meta-form-root]'))) {
            initMetaForm();
        }
    });

    document.body.addEventListener('htmx:afterSwap', function(e) {
        var target = e.detail && e.detail.target;
        if (!target) return;
        var picker = null;
        if (target.id === 'modalPickerBackdrop') {
            picker = target;
        } else if (target.querySelector && target.querySelector('#modalPickerBackdrop')) {
            picker = target.querySelector('#modalPickerBackdrop');
        }
        if (!picker) return;
        var search = picker.querySelector('[data-picker-search]');
        if (search && typeof search.focus === 'function') search.focus();
    });

    document.body.addEventListener('htmx:afterSettle', function() {
        focusMetaFormError();
    });

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', function() {
            var bs = document.getElementById('bottom-sheet-container');
            if (bs && bs.innerHTML.trim() !== '') initMetaForm();
        });
    } else {
        var bs = document.getElementById('bottom-sheet-container');
        if (bs && bs.innerHTML.trim() !== '') initMetaForm();
    }
})();
