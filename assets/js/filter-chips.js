/**
 * ContaBase Filter Chips — CSP-compliant sync
 * Sincroniza classes dos chips de filtro baseado no estado dos inputs.
 */
(function () {
    'use strict';

    function initFilterChips(form) {
        if (!form || form.dataset.ready === 'true') return;
        form.dataset.ready = 'true';

        function syncChips() {
            form.querySelectorAll('[data-filter-chip]').forEach(function (chip) {
                var input = chip.querySelector('input[type="checkbox"], input[type="radio"]');
                if (!input) return;
                var isChecked = input.checked;
                chip.classList.remove(
                    'border-violet-500', 'bg-violet-50', 'text-violet-700', 'dark:bg-violet-500/20', 'dark:text-violet-300',
                    'border-[#009866]/30', 'bg-[#009866]/10', 'text-[#009866]', 'dark:border-[#009866]/40', 'dark:bg-[#009866]/15', 'dark:text-emerald-400',
                    'border-[#FE414F]/30', 'bg-[#FE414F]/10', 'text-[#FE414F]', 'dark:border-[#FE414F]/40', 'dark:bg-[#FE414F]/15', 'dark:text-red-400',
                    'border-blue-300', 'bg-blue-50', 'text-blue-700', 'dark:border-blue-500/25', 'dark:bg-blue-500/15', 'dark:text-blue-100',
                    'border-amber-300', 'bg-amber-50', 'text-amber-700', 'dark:border-amber-500/25', 'dark:bg-amber-500/15', 'dark:text-amber-100',
                    'bg-white', 'dark:bg-zinc-900/40', 'border-zinc-200', 'dark:border-zinc-800', 'text-zinc-600', 'dark:text-zinc-400',
                    'border-emerald-300', 'border-red-300', 'border-blue-300', 'border-amber-300'
                );
                if (isChecked) {
                    var colorMap = {
                        'asc': ['border-violet-500', 'bg-violet-50', 'text-violet-700', 'dark:bg-violet-500/20', 'dark:text-violet-300'],
                        'desc': ['border-violet-500', 'bg-violet-50', 'text-violet-700', 'dark:bg-violet-500/20', 'dark:text-violet-300'],
                        'receita': ['border-[#009866]/30', 'bg-[#009866]/10', 'text-[#009866]', 'dark:border-[#009866]/40', 'dark:bg-[#009866]/15', 'dark:text-emerald-400'],
                        'despesa': ['border-[#FE414F]/30', 'bg-[#FE414F]/10', 'text-[#FE414F]', 'dark:border-[#FE414F]/40', 'dark:bg-[#FE414F]/15', 'dark:text-red-400'],
                        'transferencia': ['border-blue-300', 'bg-blue-50', 'text-blue-700', 'dark:border-blue-500/25', 'dark:bg-blue-500/15', 'dark:text-blue-100'],
                        'pendente': ['border-amber-300', 'bg-amber-50', 'text-amber-700', 'dark:border-amber-500/25', 'dark:bg-amber-500/15', 'dark:text-amber-100'],
                        'vencido': ['border-[#FE414F]/30', 'bg-[#FE414F]/10', 'text-[#FE414F]', 'dark:border-[#FE414F]/40', 'dark:bg-[#FE414F]/15', 'dark:text-red-400'],
                        'pago': ['border-[#009866]/30', 'bg-[#009866]/10', 'text-[#009866]', 'dark:border-[#009866]/40', 'dark:bg-[#009866]/15', 'dark:text-emerald-400']
                    };
                    var classes = colorMap[input.value] || ['border-violet-500', 'bg-violet-50', 'text-violet-700', 'dark:bg-violet-500/20', 'dark:text-violet-300'];
                    classes.forEach(function (cls) { chip.classList.add(cls); });
                } else {
                    ['bg-white', 'dark:bg-zinc-900/40', 'border-zinc-200', 'dark:border-zinc-800', 'text-zinc-600', 'dark:text-zinc-400'].forEach(function (cls) { chip.classList.add(cls); });
                }
            });
        }

        form.addEventListener('change', syncChips);
        form.addEventListener('submit', function (e) { e.preventDefault(); });
        syncChips();
    }

    function initAll() {
        var forms = document.querySelectorAll('form[id]');
        forms.forEach(function (form) {
            if (form.querySelector('[data-filter-chip]')) {
                initFilterChips(form);
            }
        });
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', initAll);
    } else {
        initAll();
    }

    if (!window.__contabaseFilterChipsSettleListener) {
        window.__contabaseFilterChipsSettleListener = true;
        document.addEventListener('htmx:afterSettle', function () {
            initAll();
        });
    }

})();
