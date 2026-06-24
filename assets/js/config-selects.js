(function () {
    function initProviderSelect(select) {
        if (select.hasAttribute('data-select-initialized')) return;

        var form = select.closest('[data-provider-select]');
        if (!form) return;

        syncProviderState(form, select.value);

        select.addEventListener('change', function () {
            syncProviderState(form, select.value);
        });

        select.setAttribute('data-select-initialized', '');
    }

    function syncProviderState(form, value) {
        var isCustom = value === 'custom';

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
