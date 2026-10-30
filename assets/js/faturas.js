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

    function partialFeedback(modal, input, message) {
        var errorEl = modal ? modal.querySelector('[data-partial-payment-error]') : null;
        var touched = input && input.dataset && input.dataset.partialTouched === '1';
        var visible = Boolean(message) && touched;
        if (errorEl) {
            errorEl.textContent = visible ? message : '';
            errorEl.hidden = !visible;
        }
        if (input && typeof input.setAttribute === 'function') {
            input.setAttribute('aria-invalid', (touched && message) ? 'true' : 'false');
        }
    }

    function updatePartialPaymentSummary(modal, input, output) {
        if (!modal || !input || !output) return;

        var pending = parseCentsValue(modal.getAttribute('data-pending-cents'));
        var payment = parseMoney(input.value);

        output.textContent = formatMoney(pending - payment);

        if (typeof input.setCustomValidity !== 'function') return;

        var message = '';
        if (!String(input.value || '').trim()) {
            message = 'Informe o valor do pagamento parcial.';
        } else if (payment <= 0) {
            message = 'Informe um valor maior que zero.';
        } else if (payment > pending) {
            message = 'Pagamento maior que o saldo pendente da fatura.';
        }
        input.setCustomValidity(message);
        partialFeedback(modal, input, message);
    }

    // --- Modal focus management (local to Faturas payment dialogs) ---------
    // overlay-stack.js owns history/back/ESC; these dialogs have no shared focus
    // manager, so this keeps keyboard focus inside the open dialog and returns
    // it to the trigger on close. Listeners are bound once per element.

    var settleReturnFocus = null;
    var partialReturnFocus = null;

    function dialogFocusables(root) {
        if (!root || typeof root.querySelectorAll !== 'function') return [];
        return Array.prototype.slice.call(root.querySelectorAll(
            'a[href], button:not([disabled]), select:not([disabled]), input:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'
        )).filter(function (el) {
            return el.offsetWidth > 0 || el.offsetHeight > 0 || el === document.activeElement;
        });
    }

    function trapDialogFocus(event) {
        if (event.key !== 'Tab') return;
        var root = event.currentTarget;
        var items = dialogFocusables(root);
        if (!items.length) return;
        var first = items[0];
        var last = items[items.length - 1];
        if (event.shiftKey && document.activeElement === first) {
            event.preventDefault();
            last.focus();
        } else if (!event.shiftKey && document.activeElement === last) {
            event.preventDefault();
            first.focus();
        }
    }

    function focusDialog(root, selector) {
        window.setTimeout(function () {
            var target = selector && root ? root.querySelector(selector) : null;
            if (target && typeof target.focus === 'function') target.focus();
        }, 0);
    }

    function restoreDialogFocus(element) {
        if (element && element.isConnected && typeof element.focus === 'function') {
            element.focus();
        }
    }

    // --- Global functions (called by data-onclick via event-handlers.js) ---

    window.closeSettlePaymentModal = function () {
        var settleModal = document.getElementById('invoice-settle-payment-modal');
        if (settleModal) {
            settleModal.classList.add('hidden');
            settleModal.classList.remove('flex');
        }
        restoreDialogFocus(settleReturnFocus);
        settleReturnFocus = null;
        if (typeof window.overlayDidClose === 'function') window.overlayDidClose();
    };

    window.closePartialPaymentModal = function () {
        var modal = document.getElementById('invoice-partial-payment-modal');
        if (modal) {
            modal.classList.add('hidden');
            modal.classList.remove('flex');
        }
        restoreDialogFocus(partialReturnFocus);
        partialReturnFocus = null;
        if (typeof window.overlayDidClose === 'function') window.overlayDidClose();
    };

    window.openPartialPaymentModal = function () {
        var modal = document.getElementById('invoice-partial-payment-modal');
        if (modal) {
            partialReturnFocus = document.activeElement;
            modal.classList.remove('hidden');
            modal.classList.add('flex');
            if (typeof window.overlayDidOpen === 'function') window.overlayDidOpen('partial-payment');
            focusDialog(modal, '[data-partial-payment-amount]');
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
            if (!settleModal.dataset.faturasFocusBound) {
                settleModal.dataset.faturasFocusBound = '1';
                settleModal.addEventListener('keydown', trapDialogFocus);
            }
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
                    settleReturnFocus = document.activeElement;
                    settleModal.classList.remove('hidden');
                    settleModal.classList.add('flex');
                    if (typeof window.overlayDidOpen === 'function') window.overlayDidOpen('settle-payment');
                    focusDialog(settleModal, '[data-settle-payment-account]');
                });
            }
        }

        // Partial payment modal
        if (partialModal) {
            if (!partialModal.dataset.faturasFocusBound) {
                partialModal.dataset.faturasFocusBound = '1';
                partialModal.addEventListener('keydown', trapDialogFocus);
            }
            var input = partialModal.querySelector('[data-partial-payment-amount]');
            var output = partialModal.querySelector('[data-partial-payment-remaining]');
            var form = input ? input.closest('form') : null;

            if (input && output && !input.dataset.faturasPartialBound) {
                input.dataset.faturasPartialBound = '1';
                input.addEventListener('input', function () {
                    input.dataset.partialTouched = '1';
                    setTimeout(function () {
                        updatePartialPaymentSummary(partialModal, input, output);
                    }, 0);
                });
            }
            if (form && input && output && !form.dataset.faturasPartialBound) {
                form.dataset.faturasPartialBound = '1';
                form.addEventListener('submit', function (e) {
                    input.dataset.partialTouched = '1';
                    updatePartialPaymentSummary(partialModal, input, output);
                    if (typeof form.checkValidity === 'function' && !form.checkValidity()) {
                        e.preventDefault();
                        e.stopPropagation();
                        if (typeof input.reportValidity === 'function') input.reportValidity();
                    }
                });
            }
            if (form && input && output && !form.dataset.faturasInvalidBound) {
                form.dataset.faturasInvalidBound = '1';
                // Native constraint validation blocks the submit event; reflect the
                // same message inline without changing the validation rules.
                form.addEventListener('invalid', function () {
                    input.dataset.partialTouched = '1';
                    updatePartialPaymentSummary(partialModal, input, output);
                }, true);
            }
            if (input && output) {
                updatePartialPaymentSummary(partialModal, input, output);
            }
        }
    }

    // --- Initialization ---

    function init() {
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
        // The swap target is #main-content for full-page swaps, #faturas-content for
        // OOB swaps, or #faturas-index-detail when the hub loads another card.
        if (target.id === 'main-content' || target.id === 'faturas-content' || target.id === 'faturas-index-detail' ||
            target.closest('#faturas-content') || target.closest('#faturas-index-detail')) {
            // Use setTimeout to let Lucide icons and DOM settle before binding
            setTimeout(bindPaymentModals, 10);
        }
    });

    // OOB replacements emit their own lifecycle event. Invoice mutations replace
    // #invoice-summary, including the payment buttons and modals, so direct
    // listeners from the previous fragment must be rebound after that swap.
    document.body.addEventListener('htmx:oobAfterSwap', function (e) {
        var target = (e.detail && e.detail.target) || e.target;
        if (!target) return;
        if (target.id === 'invoice-summary' || target.querySelector('#invoice-summary')) {
            setTimeout(bindPaymentModals, 10);
        }
    });
})();
