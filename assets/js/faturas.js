// Faturas — Payment modals + month selector
// Extracted from templates/pages/faturas.html inline <script> blocks (Phase D.5.2)
// Globals required by event-handlers.js data-onclick dispatch:
//   closeSettlePaymentModal, closePartialPaymentModal, openPartialPaymentModal

(function () {
    'use strict';

    // --- Money utilities (extracted from original inline) ---

    function parseMoney(raw) {
        var s = String(raw || '').replace(/[^\d,.\-]/g, '').trim();
        if (!s) return 0;
        var comma = s.lastIndexOf(',');
        var dot = s.lastIndexOf('.');
        var sep = comma > dot ? comma : dot;
        var whole = sep >= 0 ? s.slice(0, sep).replace(/[^\d\-]/g, '') : s.replace(/[^\d\-]/g, '');
        var frac = sep >= 0 ? s.slice(sep + 1).replace(/\D/g, '').slice(0, 2) : '';
        while (frac.length < 2) frac += '0';
        var cents = parseInt(whole || '0', 10) * 100 + parseInt(frac || '0', 10);
        return isNaN(cents) ? 0 : cents;
    }

    function parseCentsValue(raw) {
        var digits = String(raw || '').replace(/\D/g, '');
        if (!digits) return 0;
        var cents = parseInt(digits, 10);
        return isNaN(cents) ? 0 : cents;
    }

    function formatMoney(cents) {
        if (cents < 0) cents = 0;
        return 'R$ ' + Math.floor(cents / 100).toLocaleString('pt-BR') + ',' + String(cents % 100).padStart(2, '0');
    }

    function updatePartialPaymentSummary(modal, input, output) {
        if (!modal || !input || !output) return;

        var pending = parseCentsValue(modal.getAttribute('data-pending-cents'));
        var payment = parseMoney(input.value);

        output.textContent = formatMoney(pending - payment);

        if (typeof input.setCustomValidity !== 'function') return;

        if (!String(input.value || '').trim()) {
            input.setCustomValidity('Informe o valor do pagamento parcial.');
            return;
        }
        if (payment <= 0) {
            input.setCustomValidity('Informe um valor maior que zero.');
            return;
        }
        if (payment > pending) {
            input.setCustomValidity('Pagamento maior que o saldo pendente da fatura.');
            return;
        }
        input.setCustomValidity('');
    }

    // --- Global functions (called by data-onclick via event-handlers.js) ---

    window.closeSettlePaymentModal = function () {
        var settleModal = document.getElementById('invoice-settle-payment-modal');
        if (settleModal) {
            settleModal.classList.add('hidden');
            settleModal.classList.remove('flex');
        }
        if (typeof window.overlayDidClose === 'function') window.overlayDidClose();
    };

    window.closePartialPaymentModal = function () {
        var modal = document.getElementById('invoice-partial-payment-modal');
        if (modal) {
            modal.classList.add('hidden');
            modal.classList.remove('flex');
        }
        if (typeof window.overlayDidClose === 'function') window.overlayDidClose();
    };

    window.openPartialPaymentModal = function () {
        var modal = document.getElementById('invoice-partial-payment-modal');
        if (modal) {
            modal.classList.remove('hidden');
            modal.classList.add('flex');
            if (typeof window.overlayDidOpen === 'function') window.overlayDidOpen('partial-payment');
        }
    };

    // --- ESC key handler (registered once globally) ---

    if (!window._faturasEscBound) {
        window._faturasEscBound = true;
        document.addEventListener('keydown', function (e) {
            if (e.key !== 'Escape') return;
            var partialModal = document.getElementById('invoice-partial-payment-modal');
            if (partialModal && !partialModal.classList.contains('hidden')) {
                e.stopImmediatePropagation();
                window.closePartialPaymentModal();
                return;
            }
            var settleModal = document.getElementById('invoice-settle-payment-modal');
            if (settleModal && !settleModal.classList.contains('hidden')) {
                e.stopImmediatePropagation();
                window.closeSettlePaymentModal();
                return;
            }
        });
    }

    // --- Settle modal helpers ---

    function updateSettleSummary(selectEl, balanceEl) {
        if (!selectEl || !balanceEl) return;
        var option = selectEl.options[selectEl.selectedIndex];
        balanceEl.textContent = option ? option.getAttribute('data-balance-after') || '-' : '-';
    }

    // --- Payment modals binding (idempotent, HTMX-safe) ---

    function bindPaymentModals() {
        var settleModal = document.getElementById('invoice-settle-payment-modal');
        var partialModal = document.getElementById('invoice-partial-payment-modal');

        if (!settleModal && !partialModal) return; // page without payment actions

        // Settle modal
        if (settleModal) {
            var settleSelect = settleModal.querySelector('[data-settle-payment-account]');
            var settleBalanceAfter = settleModal.querySelector('[data-settle-balance-after]');

            if (settleSelect && !settleSelect.dataset.faturasSettleBound) {
                settleSelect.dataset.faturasSettleBound = '1';
                settleSelect.addEventListener('change', function () {
                    updateSettleSummary(settleSelect, settleBalanceAfter);
                });
            }
            if (settleSelect && settleBalanceAfter) {
                updateSettleSummary(settleSelect, settleBalanceAfter);
            }

            var settleButton = document.querySelector('[data-open-settle-payment]');
            if (settleButton && !settleButton.dataset.faturasSettleBtnBound) {
                settleButton.dataset.faturasSettleBtnBound = '1';
                settleButton.addEventListener('click', function () {
                    var sel = settleModal.querySelector('[data-settle-payment-account]');
                    var bal = settleModal.querySelector('[data-settle-balance-after]');
                    if (!sel || !sel.value) return;
                    if (sel && bal) updateSettleSummary(sel, bal);
                    settleModal.classList.remove('hidden');
                    settleModal.classList.add('flex');
                    if (typeof window.overlayDidOpen === 'function') window.overlayDidOpen('settle-payment');
                });
            }
        }

        // Partial payment modal
        if (partialModal) {
            var input = partialModal.querySelector('[data-partial-payment-amount]');
            var output = partialModal.querySelector('[data-partial-payment-remaining]');
            var form = input ? input.closest('form') : null;

            if (input && output && !input.dataset.faturasPartialBound) {
                input.dataset.faturasPartialBound = '1';
                input.addEventListener('input', function () {
                    setTimeout(function () {
                        updatePartialPaymentSummary(partialModal, input, output);
                    }, 0);
                });
            }
            if (form && input && output && !form.dataset.faturasPartialBound) {
                form.dataset.faturasPartialBound = '1';
                form.addEventListener('submit', function (e) {
                    updatePartialPaymentSummary(partialModal, input, output);
                    if (typeof form.checkValidity === 'function' && !form.checkValidity()) {
                        e.preventDefault();
                        e.stopPropagation();
                        if (typeof input.reportValidity === 'function') input.reportValidity();
                    }
                });
            }
            if (input && output) {
                updatePartialPaymentSummary(partialModal, input, output);
            }
        }
    }

    // --- Month selector ---

    function centerActiveMonth() {
        var container = document.getElementById('month-selector-container');
        if (!container) return;
        var active = container.querySelector('[data-month-active="true"]');
        if (active) {
            var containerWidth = container.offsetWidth;
            var activeWidth = active.offsetWidth;
            var activeOffset = active.getBoundingClientRect().left - container.getBoundingClientRect().left + container.scrollLeft;
            container.scroll({
                left: activeOffset - (containerWidth / 2) + (activeWidth / 2),
                behavior: 'smooth'
            });
        }
    }

    // --- Initialization ---

    function init() {
        // Month selector (once)
        setTimeout(centerActiveMonth, 50);

        if (!window.monthSelectorInitialized) {
            window.monthSelectorInitialized = true;
            document.body.addEventListener('htmx:afterSettle', function () {
                centerActiveMonth();
            });
        }

        // Payment modals (may need re-binding after HTMX swaps)
        bindPaymentModals();
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', init);
    } else {
        init();
    }

    // Re-bind payment modals after HTMX swaps that include faturas content
    document.body.addEventListener('htmx:afterSwap', function (e) {
        var target = e.detail && e.detail.target;
        if (!target) return;
        // The swap target is #main-content for full-page swaps, or #faturas-content for OOB swaps
        if (target.id === 'main-content' || target.id === 'faturas-content' || target.closest('#faturas-content')) {
            // Use setTimeout to let Lucide icons and DOM settle before binding
            setTimeout(bindPaymentModals, 10);
        }
    });
})();
