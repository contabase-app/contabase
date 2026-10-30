// Form Meta — extracted from templates/components/form_meta.html (Fase C2)
// CSP-safe external JS: no inline scripts, uses data-* attributes and event delegation.
// M6: o segmented usa os contratos .ui-tab/is-active/aria-pressed e a validação
// client-side cobre apenas obrigatoriedade e valor maior que zero. As regras
// financeiras (saldo, limite, moeda, competência) continuam no backend.
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

    function categoryPicker(suffix) {
        return document.querySelector('#categoriaPicker' + suffix + ' [data-meta-category-picker]');
    }

    function categoryErrorId(suffix) {
        return suffix === '_caixinha' ? 'categoria-error-caixinha' : 'categoria-error-limite';
    }

    function clearCategoryState(suffix) {
        clearMetaFieldError(categoryErrorId(suffix));
        setMetaInvalid(categoryPicker(suffix), false);
    }

    function clearMetaValidationState() {
        clearCategoryState('_limite');
        clearCategoryState('_caixinha');
        clearMetaFieldError('limite-amount-error');
        clearMetaFieldError('caixinha-name-error');
        setMetaInvalid(document.getElementById('limiteMaxAmount'), false);
        setMetaInvalid(document.getElementById('caixinhaName'), false);
    }

    function focusMetaFormError() {
        var error = document.querySelector('#limite-form-error:not(.hidden), #caixinha-form-error:not(.hidden)');
        if (!error || error.dataset.metaErrorFocused === 'true') return;
        error.dataset.metaErrorFocused = 'true';
        if (!error.hasAttribute('tabindex')) error.setAttribute('tabindex', '-1');
        error.focus();
    }

    function validateMetaForm(form) {
        if (!form) return true;
        if (form.id === 'meta-limite-form') {
            var category = document.getElementById('categoriaId_limite');
            var picker = categoryPicker('_limite');
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

        if (form.id === 'meta-caixinha-form') {
            var name = document.getElementById('caixinhaName');
            clearMetaFieldError('caixinha-name-error');
            setMetaInvalid(name, false);
            if (!name || !String(name.value || '').trim()) {
                setMetaFieldError('caixinha-name-error', 'Informe o nome da reserva.');
                setMetaInvalid(name, true);
                if (name && typeof name.focus === 'function') name.focus();
                return false;
            }
            return true;
        }

        return true;
    }

    window.toggleMetaTab = function(tab) {
        var isCaixinha = tab === 'caixinha';
        var caixinhaForm = document.getElementById('meta-caixinha-form');
        var limiteForm = document.getElementById('meta-limite-form');
        var caixinhaTab = document.getElementById('tab-caixinha');
        var limiteTab = document.getElementById('tab-limite');
        if (!caixinhaForm || !limiteForm || !caixinhaTab || !limiteTab) return;
        caixinhaForm.classList.toggle('hidden', !isCaixinha);
        limiteForm.classList.toggle('hidden', isCaixinha);

        var caixinhaName = caixinhaForm.querySelector('[name="name"]');
        var limiteAmount = limiteForm.querySelector('[name="max_amount_monthly"]');
        if (caixinhaName) {
            caixinhaName.required = isCaixinha;
            caixinhaName.setAttribute('aria-required', isCaixinha ? 'true' : 'false');
        }
        if (limiteAmount) {
            limiteAmount.required = !isCaixinha;
            limiteAmount.setAttribute('aria-required', !isCaixinha ? 'true' : 'false');
        }

        caixinhaTab.classList.toggle('is-active', isCaixinha);
        limiteTab.classList.toggle('is-active', !isCaixinha);
        caixinhaTab.setAttribute('aria-pressed', isCaixinha ? 'true' : 'false');
        limiteTab.setAttribute('aria-pressed', isCaixinha ? 'false' : 'true');

        var root = document.querySelector('[data-meta-form-root]');
        if (!root) return;
        root.dataset.openTab = isCaixinha ? 'caixinha' : 'limite';
        var createSubmit = root.querySelector('[data-meta-create-submit]');
        if (createSubmit) {
            createSubmit.setAttribute('form', 'meta-' + tab + '-form');
        }
        var submitLabel = root.querySelector('[data-meta-submit-label]');
        if (submitLabel) {
            submitLabel.textContent = isCaixinha ? (root.dataset.labelCaixinha || 'Criar reserva') : (root.dataset.labelLimite || 'Criar limite');
        }
        var title = document.getElementById('meta-sheet-title');
        var description = document.getElementById('meta-sheet-description');
        var eyebrow = root.querySelector('[data-meta-sheet-eyebrow]');
        if (title && root.dataset.titleLimite) {
            title.textContent = isCaixinha ? (root.dataset.titleCaixinha || 'Nova reserva') : root.dataset.titleLimite;
        }
        if (description && root.dataset.descriptionLimite) {
            description.textContent = isCaixinha ? (root.dataset.descriptionCaixinha || '') : root.dataset.descriptionLimite;
        }
        if (eyebrow && root.dataset.eyebrowLimite) {
            eyebrow.textContent = isCaixinha ? (root.dataset.eyebrowCaixinha || 'Reservas') : root.dataset.eyebrowLimite;
        }
        var sheetIcon = root.querySelector('[data-meta-sheet-icon]');
        if (sheetIcon) {
            sheetIcon.setAttribute('data-lucide', isCaixinha ? 'piggy-bank' : 'gauge');
            if (typeof window.refreshIcons === 'function') window.refreshIcons();
        }
        var closeButton = root.querySelector('.metas-sheet__close');
        if (closeButton) {
            closeButton.setAttribute('aria-label', isCaixinha ? 'Fechar formulário de reserva' : 'Fechar formulário de limite');
        }
        clearMetaValidationState();
    };

    function initMetaSelectPickerItem() {
        window.selectPickerItem = function(id, name, icon, color) {
            var caixinhaForm = document.getElementById('meta-caixinha-form');
            // Only handle if meta forms are in DOM; otherwise let other forms' selectPickerItem handle it.
            if (!caixinhaForm) return;

            var isCaixinha = !caixinhaForm.classList.contains('hidden');
            var suffix = isCaixinha ? '_caixinha' : '_limite';

            var catIdEl = document.getElementById('categoriaId' + suffix);
            if (!catIdEl) return;
            catIdEl.value = id;
            clearCategoryState(suffix);

            var nomeEl = document.getElementById('categoriaNome' + suffix);
            if (nomeEl) nomeEl.innerText = name;

            var iconEl = document.getElementById('categoriaIcon' + suffix);
            if (iconEl) {
                iconEl.setAttribute('data-lucide', icon);
                if (typeof window.applyRuntimeColor === 'function') window.applyRuntimeColor(iconEl, color);
            }

            var wrap = document.getElementById('categoriaIconWrap' + suffix);
            if (wrap && typeof window.applyRuntimeColor === 'function') window.applyRuntimeColor(wrap, color);

            pickerTrigger = null;
            var pickerButton = categoryPicker(suffix);
            if (pickerButton && typeof pickerButton.focus === 'function') pickerButton.focus();

            if (typeof window.refreshIcons === 'function') window.refreshIcons();
        };
    }

    function initMonthPicker() {
        var monthInput = document.getElementById('targetMonthInput');
        if (!monthInput || monthInput.dataset.metaMonthPickerReady) return;
        monthInput.dataset.metaMonthPickerReady = 'true';
        monthInput.addEventListener('click', function() {
            if (typeof monthInput.showPicker === 'function') monthInput.showPicker();
        });
    }

    function initMetaTabs(root) {
        if (!root) return;
        root.querySelectorAll('[data-meta-tab]:not([data-meta-tab-ready])').forEach(function(tabButton) {
            tabButton.dataset.metaTabReady = 'true';
            tabButton.addEventListener('click', function() {
                window.toggleMetaTab(tabButton.dataset.metaTab || 'limite');
            });
        });
    }

    function clearMetaCategory(suffix) {
        var catIdEl = document.getElementById('categoriaId' + suffix);
        if (catIdEl) catIdEl.value = '';

        var nomeEl = document.getElementById('categoriaNome' + suffix);
        if (nomeEl) nomeEl.innerText = suffix === '_caixinha' ? 'Sem categoria' : 'Buscar categoria...';

        var iconEl = document.getElementById('categoriaIcon' + suffix);
        if (iconEl) {
            iconEl.setAttribute('data-lucide', suffix === '_caixinha' ? 'piggy-bank' : 'tag');
            if (typeof window.applyRuntimeColor === 'function') window.applyRuntimeColor(iconEl, '#6B7280');
        }

        var wrap = document.getElementById('categoriaIconWrap' + suffix);
        if (wrap && typeof window.applyRuntimeColor === 'function') window.applyRuntimeColor(wrap, '#6B7280');

        clearCategoryState(suffix);
        if (typeof window.refreshIcons === 'function') window.refreshIcons();
    }

    function isMetaFormInDOM() {
        return !!document.querySelector('[data-meta-form-root]');
    }

    function initMetaForm() {
        if (!isMetaFormInDOM()) return;

        initMetaSelectPickerItem();
        initMonthPicker();

        var article = document.querySelector('[data-meta-form-root]');
        initMetaTabs(article);
        var openTab = article ? article.getAttribute('data-open-tab') : null;
        window.toggleMetaTab(openTab || 'limite');
    }

    document.addEventListener('click', function(e) {
        var button = e.target.closest ? e.target.closest('[data-meta-category-picker]') : null;
        if (!button) return;
        pickerTrigger = button;
        installPickerFocusBridge();
    }, true);

    document.body.addEventListener('click', function(e) {
        var clearButton = e.target.closest('[data-clear-meta-category]');
        if (!clearButton) return;
        clearMetaCategory(clearButton.getAttribute('data-clear-meta-category'));
    });

    document.addEventListener('submit', function(e) {
        var form = e.target;
        if (!form || (form.id !== 'meta-limite-form' && form.id !== 'meta-caixinha-form')) return;
        if (validateMetaForm(form)) return;
        e.preventDefault();
        e.stopImmediatePropagation();
    }, true);

    document.addEventListener('input', function(e) {
        if (e.target.id === 'limiteMaxAmount') {
            clearMetaFieldError('limite-amount-error');
            setMetaInvalid(e.target, false);
        }
        if (e.target.id === 'caixinhaName') {
            clearMetaFieldError('caixinha-name-error');
            setMetaInvalid(e.target, false);
        }
    });

    // Initialize when meta form is swapped into bottom sheet
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

    // Run on initial page load if meta form already in DOM
    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', function() {
            var bs = document.getElementById('bottom-sheet-container');
            if (bs && bs.innerHTML.trim() !== '') {
                initMetaForm();
            }
        });
    } else {
        var bs = document.getElementById('bottom-sheet-container');
        if (bs && bs.innerHTML.trim() !== '') {
            initMetaForm();
        }
    }
})();
