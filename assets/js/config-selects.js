(function () {
    function initProviderSelect(select) {
        if (select.hasAttribute('data-select-initialized')) return;

        var form = select.closest('[data-provider-select]');
        if (!form) return;

        initProviderDialog(form, select);
        initProviderChoices(form, select);
        syncProviderState(form, select.value);

        select.addEventListener('change', function () {
            syncProviderState(form, select.value);
        });

        select.setAttribute('data-select-initialized', '');
    }

    function syncProviderState(form, value) {
        var isCustom = value === 'custom';

        form.querySelectorAll('[data-provider-choice]').forEach(function (choice) {
            choice.setAttribute('aria-pressed', String(choice.dataset.providerSlug === value));
        });

        syncProviderTrigger(form, value);

        var customSection = form.querySelector('[data-provider-custom]');
        if (customSection) {
            if (isCustom) {
                customSection.classList.remove('hidden');
            } else {
                customSection.classList.add('hidden');
            }
        }

        form.querySelectorAll('[data-provider-custom-input]').forEach(function (input) {
            if (isCustom) {
                input.removeAttribute('disabled');
            } else {
                input.setAttribute('disabled', '');
            }
        });
    }

    function initProviderChoices(form, select) {
        form.querySelectorAll('[data-provider-choice]').forEach(function (choice) {
            if (choice.hasAttribute('data-provider-choice-initialized')) return;

            choice.addEventListener('click', function () {
                var value = choice.dataset.providerSlug || 'custom';
                if (select.value !== value) {
                    select.value = value;
                    select.dispatchEvent(new Event('change', { bubbles: true }));
                }
                closeProviderDialog(form);
            });

            choice.setAttribute('data-provider-choice-initialized', '');
        });
    }

    function initProviderDialog(form) {
        var dialog = form.querySelector('[data-provider-dialog]');
        var openButton = form.querySelector('[data-provider-open]');
        if (!dialog || !openButton || dialog.hasAttribute('data-provider-dialog-initialized')) return;

        var closeButton = dialog.querySelector('[data-provider-close]');
        var searchInput = dialog.querySelector('[data-provider-search]');

        openButton.addEventListener('click', function () {
            resetProviderSearch(form);
            if (typeof dialog.showModal === 'function') {
                dialog.showModal();
            } else {
                dialog.setAttribute('open', '');
            }
            openButton.setAttribute('aria-expanded', 'true');
            if (searchInput) searchInput.focus();
        });

        if (closeButton) {
            closeButton.addEventListener('click', function () {
                closeProviderDialog(form);
            });
        }

        dialog.addEventListener('click', function (event) {
            if (event.target === dialog) closeProviderDialog(form);
        });

        dialog.addEventListener('close', function () {
            openButton.setAttribute('aria-expanded', 'false');
        });

        if (searchInput) {
            searchInput.addEventListener('input', function () {
                filterProviderChoices(form, searchInput.value);
            });
        }

        dialog.setAttribute('data-provider-dialog-initialized', '');
    }

    function closeProviderDialog(form) {
        var dialog = form.querySelector('[data-provider-dialog]');
        var openButton = form.querySelector('[data-provider-open]');
        if (!dialog) return;

        if (typeof dialog.close === 'function' && dialog.open) {
            dialog.close();
        } else {
            dialog.removeAttribute('open');
            if (openButton) openButton.setAttribute('aria-expanded', 'false');
        }
        if (openButton) openButton.focus();
    }

    function resetProviderSearch(form) {
        var searchInput = form.querySelector('[data-provider-search]');
        if (searchInput) searchInput.value = '';
        filterProviderChoices(form, '');
    }

    function filterProviderChoices(form, query) {
        var normalized = String(query || '').trim().toLocaleLowerCase('pt-BR');
        var visibleProviders = 0;

        form.querySelectorAll('[data-provider-choice]').forEach(function (choice) {
            var isCustom = choice.hasAttribute('data-provider-custom-choice');
            var searchText = (choice.dataset.providerSearchText || '').toLocaleLowerCase('pt-BR');
            var visible = isCustom || normalized === '' || searchText.indexOf(normalized) !== -1;
            choice.classList.toggle('hidden', !visible);
            if (visible && !isCustom) visibleProviders += 1;
        });

        var emptyState = form.querySelector('[data-provider-empty]');
        if (emptyState) emptyState.classList.toggle('hidden', visibleProviders !== 0 || normalized === '');
    }

    function syncProviderTrigger(form, value) {
        var choice = null;
        form.querySelectorAll('[data-provider-choice]').forEach(function (item) {
            if (item.dataset.providerSlug === value) choice = item;
        });

        var logoWrap = form.querySelector('[data-provider-trigger-logo]');
        var logoImage = form.querySelector('[data-provider-trigger-image]');
        var logoFallback = form.querySelector('[data-provider-trigger-fallback]');
        var placeholder = form.querySelector('[data-provider-trigger-placeholder]');
        var name = form.querySelector('[data-provider-trigger-name]');
        var hint = form.querySelector('[data-provider-trigger-hint]');

        if (logoImage) {
            logoImage.classList.add('hidden');
            logoImage.removeAttribute('src');
        }
        if (logoFallback) {
            logoFallback.classList.add('hidden');
            logoFallback.textContent = '';
        }
        if (placeholder) placeholder.classList.remove('hidden');
        if (logoWrap && typeof window.clearRuntimeColor === 'function') window.clearRuntimeColor(logoWrap);

        if (!choice) {
            if (name) name.textContent = 'Escolha a instituição';
            if (hint) hint.textContent = 'Toque para ver as opções disponíveis';
            return;
        }

        var providerName = choice.dataset.providerName || 'Outra / Personalizada';
        var providerLogo = choice.dataset.providerLogo || '';
        var providerMark = choice.dataset.providerMark || '';
        var providerColor = choice.dataset.providerColor || '';

        if (name) name.textContent = providerName;
        if (hint) hint.textContent = value === 'custom' ? 'Instituição personalizada' : 'Instituição selecionada';

        if (providerLogo && logoImage) {
            logoImage.src = providerLogo;
            logoImage.classList.remove('hidden');
            if (logoFallback) {
                logoFallback.textContent = providerMark;
                logoFallback.className = 'hidden emblem-text emblem-len-' + providerMark.length;
            }
            if (placeholder) placeholder.classList.add('hidden');
            if (logoWrap && providerColor && typeof window.applyRuntimeColor === 'function') {
                window.applyRuntimeColor(logoWrap, providerColor);
            }
        }
    }

    function initParentSelect(select) {
        if (select.hasAttribute('data-select-initialized')) return;

        var form = select.closest('[data-parent-select]');
        if (!form) return;

        syncParentState(form, select.value);

        select.addEventListener('change', function () {
            syncParentState(form, select.value);
        });

        select.setAttribute('data-select-initialized', '');
    }

    function syncParentState(form, value) {
        var hasParent = value !== '';

        form.querySelectorAll('[data-parent-dependent]').forEach(function (el) {
            if (hasParent) {
                el.setAttribute('disabled', '');
                el.classList.add('opacity-60', 'cursor-not-allowed');
            } else {
                el.removeAttribute('disabled');
                el.classList.remove('opacity-60', 'cursor-not-allowed');
            }
        });

        var hint = form.querySelector('[data-parent-hint]');
        if (hint) {
            if (hasParent) {
                hint.classList.remove('hidden');
            } else {
                hint.classList.add('hidden');
            }
        }
    }

    function initAll() {
        document.querySelectorAll('[data-provider-value]').forEach(initProviderSelect);
        document.querySelectorAll('[data-parent-value]').forEach(initParentSelect);
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', initAll);
    } else {
        initAll();
    }

    document.body.addEventListener('htmx:afterSettle', initAll);
})();
