// Transaction payment confirmation modal.
// Opens one shared modal for pending -> paid actions from click or swipe.
(function () {
    'use strict';

    function localDateInputValue(date) {
        var d = date || new Date();
        var local = new Date(d.getTime() - d.getTimezoneOffset() * 60000);
        return local.toISOString().slice(0, 10);
    }

    function formatDateLabel(value) {
        var parts = String(value || '').split('-');
        if (parts.length !== 3) return '--/--/----';
        return parts[2] + '/' + parts[1] + '/' + parts[0];
    }

    function findPaymentSource(el) {
        if (!el || !el.closest) return null;
        return el.closest('[data-payment-id]');
    }

    function getModalParts() {
        var modal = document.getElementById('payment-confirm-modal');
        var form = document.getElementById('payment-confirm-form');
        var dateInput = document.getElementById('payment-confirm-date');
        var scheduledDateInput = document.getElementById('payment-scheduled-date');
        return {
            modal: modal,
            form: form,
            dateInput: dateInput,
            scheduledDateInput: scheduledDateInput,
            paidDateRow: document.getElementById('payment-paid-date-row'),
            scheduledDateRow: document.getElementById('payment-scheduled-date-row'),
            clearScheduleOption: document.getElementById('payment-clear-schedule-option'),
            context: document.getElementById('payment-confirm-context'),
            title: document.getElementById('payment-confirm-title'),
            description: document.getElementById('payment-confirm-description'),
            summary: document.getElementById('payment-confirm-summary'),
            submit: document.getElementById('payment-confirm-submit'),
            accountReadonlyRow: document.getElementById('payment-account-readonly-row'),
            accountReadonly: document.getElementById('payment-account-readonly'),
            accountReadonlyEffect: document.getElementById('payment-account-readonly-effect'),
            accountHint: document.getElementById('payment-account-hint'),
            accountSelectRow: document.getElementById('payment-account-select-row'),
            accountSelect: document.getElementById('payment-account-select'),
            accountSelectEffect: document.getElementById('payment-account-select-effect'),
            accountLabels: document.querySelectorAll('[data-payment-account-label]')
        };
    }

    function selectedAction(form) {
        var checked = form && form.querySelector('input[name="payment_action"]:checked');
        return checked ? checked.value : 'paid';
    }

    function clearBulkInputs(form) {
        if (!form) return;
        form.querySelectorAll('[data-payment-bulk-input]').forEach(function (el) {
            el.remove();
        });
    }

    function addHiddenInput(form, name, value) {
        var input = document.createElement('input');
        input.type = 'hidden';
        input.name = name;
        input.value = value;
        input.setAttribute('data-payment-bulk-input', '');
        form.appendChild(input);
    }

    function configurePaymentAccount(source) {
        var parts = getModalParts();
        if (!parts.form) return;
        var accountID = source ? (source.getAttribute('data-payment-account-id') || '') : '';
        var accountName = source ? (source.getAttribute('data-payment-account-name') || 'Conta vinculada') : 'Contas dos lançamentos selecionados';
        var canChange = source && source.getAttribute('data-payment-account-change-allowed') === 'true';
        var paymentType = source ? (source.getAttribute('data-payment-type') || '') : '';

        parts.form.dataset.paymentAccountID = accountID;
        parts.form.dataset.paymentAccountName = accountName;
        parts.form.dataset.paymentAccountChangeAllowed = canChange ? 'true' : 'false';
        parts.form.dataset.paymentType = paymentType;
        if (parts.accountReadonly) parts.accountReadonly.textContent = accountName;
        if (!parts.accountSelect) return;

        parts.accountSelect.innerHTML = '';
        var options = document.getElementById('payment-account-options');
        if (options) {
            Array.prototype.forEach.call(options.options, function (option) {
                parts.accountSelect.appendChild(option.cloneNode(true));
            });
        }
        parts.accountSelect.value = accountID;
        if (parts.accountSelect.value !== accountID) canChange = false;
        parts.form.dataset.paymentAccountChangeAllowed = canChange ? 'true' : 'false';
    }

    function updatePaymentAccountMode(parts, action) {
        if (!parts || !parts.form) return;
        var canChange = parts.form.dataset.paymentAccountChangeAllowed === 'true' && action === 'paid';
        var isIncome = parts.form.dataset.paymentType === 'INCOME';
        var accountLabel = isIncome ? 'Conta de recebimento' : 'Conta de pagamento';
        var accountEffect = isIncome ? 'Será creditado nesta conta.' : 'Será debitado desta conta.';
        Array.prototype.forEach.call(parts.accountLabels || [], function (label) {
            label.textContent = accountLabel;
        });
        if (parts.accountSelectRow) parts.accountSelectRow.classList.toggle('hidden', !canChange);
        if (parts.accountReadonlyRow) parts.accountReadonlyRow.classList.toggle('hidden', canChange);
        if (parts.accountSelect) parts.accountSelect.disabled = !canChange;
        if (parts.accountSelectEffect) parts.accountSelectEffect.textContent = accountEffect;
        if (parts.accountReadonlyEffect) {
            parts.accountReadonlyEffect.textContent = action === 'paid' ? accountEffect : 'A conta vinculada será mantida ao agendar.';
        }
        if (parts.accountHint) {
            parts.accountHint.classList.toggle('hidden', action !== 'paid');
            parts.accountHint.textContent = 'Para trocar a conta, edite o lançamento antes de pagar.';
        }
    }

    function updateSummary() {
        var parts = getModalParts();
        if (!parts.summary || !parts.form) return;
        var action = selectedAction(parts.form);
        if (parts.paidDateRow) parts.paidDateRow.classList.toggle('hidden', action !== 'paid');
        if (parts.scheduledDateRow) parts.scheduledDateRow.classList.toggle('hidden', action !== 'scheduled');
        if (parts.dateInput) parts.dateInput.required = action === 'paid';
        if (parts.scheduledDateInput) parts.scheduledDateInput.required = action === 'scheduled';

        updatePaymentAccountMode(parts, action);
        if (action === 'scheduled') {
            parts.summary.textContent = 'Continuará pendente e ficará agendado para ' + formatDateLabel(parts.scheduledDateInput && parts.scheduledDateInput.value);
            if (parts.submit) parts.submit.innerHTML = '<i data-lucide="timer" class="h-4 w-4"></i>Agendar pagamento';
            refreshIcons();
            return;
        }
        if (action === 'clear_schedule') {
            parts.summary.textContent = 'O lançamento continuará pendente sem agendamento ativo.';
            if (parts.submit) parts.submit.innerHTML = '<i data-lucide="x" class="h-4 w-4"></i>Remover agendamento';
            refreshIcons();
            return;
        }
        if (parts.context && parts.context.value === 'bulk-lancamentos') {
            parts.summary.textContent = 'Serão marcados como pagos em ' + formatDateLabel(parts.dateInput && parts.dateInput.value) + '.';
        } else if (parts.form.dataset.paymentType === 'INCOME') {
            parts.summary.textContent = 'Será recebido em ' + formatDateLabel(parts.dateInput && parts.dateInput.value) + '.';
        } else {
            parts.summary.textContent = 'Será pago em ' + formatDateLabel(parts.dateInput && parts.dateInput.value) + '.';
        }
        if (parts.submit) parts.submit.innerHTML = '<i data-lucide="check" class="h-4 w-4"></i>Pagar agora';
        refreshIcons();
    }

    function refreshIcons() {
        if (typeof window.refreshIcons === 'function') {
            window.refreshIcons();
            return;
        }
        if (window.lucide && typeof window.lucide.createIcons === 'function') {
            window.lucide.createIcons();
        }
    }

    window.closePaymentConfirmModal = function () {
        var parts = getModalParts();
        if (parts.modal) {
            parts.modal.classList.add('hidden');
            parts.modal.classList.remove('flex');
        }
        if (typeof window.overlayDidClose === 'function') window.overlayDidClose();
    };

    window.openPaymentConfirmModalFromElement = function (el) {
        var source = findPaymentSource(el);
        var parts = getModalParts();
        if (!source || !parts.modal || !parts.form || !parts.dateInput) return;
        clearBulkInputs(parts.form);

        var txId = source.getAttribute('data-payment-id') || '';
        var context = source.getAttribute('data-payment-context') || 'lancamentos';
        var postURL = source.getAttribute('data-payment-post-url') || ('/transacoes/' + txId + '/status-pagamento');
        var target = source.getAttribute('data-payment-target') || ('#tx-' + txId);
        var swap = source.getAttribute('data-payment-swap') || 'outerHTML';
        var desc = source.getAttribute('data-payment-description') || 'Lançamento';
        var amount = source.getAttribute('data-payment-amount') || '';
        var scheduledAt = source.getAttribute('data-payment-scheduled-at') || '';

        configurePaymentAccount(source);
        if (parts.submit) parts.submit.disabled = false;
        parts.form.removeAttribute('aria-busy');

        parts.form.setAttribute('hx-post', postURL);
        parts.form.setAttribute('hx-target', target);
        parts.form.setAttribute('hx-swap', swap);
        parts.form.removeAttribute('data-hx-after');
        if (parts.context) parts.context.value = context;
        var paidMode = parts.form.querySelector('input[name="payment_action"][value="paid"]');
        if (paidMode) paidMode.checked = true;
        parts.dateInput.value = localDateInputValue();
        parts.dateInput.setCustomValidity('');
        if (parts.scheduledDateInput) {
            parts.scheduledDateInput.value = localDateInputValue();
            parts.scheduledDateInput.setCustomValidity('');
        }
        if (parts.clearScheduleOption) {
            parts.clearScheduleOption.classList.toggle('hidden', !scheduledAt);
            parts.clearScheduleOption.classList.toggle('flex', !!scheduledAt);
        }
        if (parts.title) parts.title.textContent = 'Confirmar pagamento';
        if (parts.description) parts.description.textContent = amount ? desc + ' · ' + amount : desc;

        updateSummary();
        if (window.htmx && typeof window.htmx.process === 'function') {
            window.htmx.process(parts.form);
        }
        parts.modal.classList.remove('hidden');
        parts.modal.classList.add('flex');
        if (typeof window.overlayDidOpen === 'function') window.overlayDidOpen('payment-confirm');
        try {
            parts.dateInput.focus({ preventScroll: true });
        } catch (ignore) {
            parts.dateInput.focus();
        }
        refreshIcons();
    };

    window.openBulkPaymentConfirmModal = function (options) {
        var parts = getModalParts();
        if (!parts.modal || !parts.form || !parts.dateInput) return;

        options = options || {};
        var ids = Array.isArray(options.ids) ? options.ids.filter(Boolean) : [];
        if (ids.length === 0) return;

        clearBulkInputs(parts.form);

        configurePaymentAccount(null);
        if (parts.submit) parts.submit.disabled = false;
        parts.form.removeAttribute('aria-busy');
        parts.form.setAttribute('hx-post', options.postURL || '/transacoes/bulk/update');
        parts.form.setAttribute('hx-target', options.target || '#lancamentos-table-body');
        parts.form.setAttribute('hx-swap', options.swap || 'outerHTML');
        parts.form.setAttribute('data-hx-after', 'resetBulkSelection');
        if (parts.context) parts.context.value = 'bulk-lancamentos';
        var paidMode = parts.form.querySelector('input[name="payment_action"][value="paid"]');
        if (paidMode) paidMode.checked = true;
        if (parts.clearScheduleOption) {
            parts.clearScheduleOption.classList.add('hidden');
            parts.clearScheduleOption.classList.remove('flex');
        }
        addHiddenInput(parts.form, 'status_pagamento', 'paid');
        ids.forEach(function (id) {
            addHiddenInput(parts.form, 'ids[]', id);
        });

        parts.dateInput.value = localDateInputValue();
        parts.dateInput.setCustomValidity('');
        if (parts.scheduledDateInput) {
            parts.scheduledDateInput.value = localDateInputValue();
            parts.scheduledDateInput.setCustomValidity('');
        }
        if (parts.title) parts.title.textContent = 'Confirmar pagamentos';
        if (parts.description) parts.description.textContent = 'Marcar ' + ids.length + ' lançamento' + (ids.length === 1 ? '' : 's') + ' como pagos';

        updateSummary();
        if (window.htmx && typeof window.htmx.process === 'function') {
            window.htmx.process(parts.form);
        }
        parts.modal.classList.remove('hidden');
        parts.modal.classList.add('flex');
        if (typeof window.overlayDidOpen === 'function') window.overlayDidOpen('payment-confirm');
        try {
            parts.dateInput.focus({ preventScroll: true });
        } catch (ignore) {
            parts.dateInput.focus();
        }
        refreshIcons();
    };

    window.openPaymentConfirmModal = function (txId) {
        var el = document.querySelector('[data-payment-id="' + txId + '"]');
        if (el) window.openPaymentConfirmModalFromElement(el);
    };

    document.addEventListener('click', function (event) {
        var trigger = event.target.closest && event.target.closest('[data-payment-confirm]');
        if (!trigger) return;
        event.preventDefault();
        event.stopPropagation();
        window.openPaymentConfirmModalFromElement(trigger);
    }, true);

    document.addEventListener('input', function (event) {
        if (event.target && (event.target.id === 'payment-confirm-date' || event.target.id === 'payment-scheduled-date')) updateSummary();
    });

    document.addEventListener('change', function (event) {
        if (event.target && (event.target.id === 'payment-confirm-date' || event.target.id === 'payment-scheduled-date' || event.target.id === 'payment-account-select' || event.target.hasAttribute('data-payment-mode'))) updateSummary();
    });

    document.addEventListener('submit', function (event) {
        var parts = getModalParts();
        if (!parts.form || event.target !== parts.form || !parts.dateInput) return;
        var action = selectedAction(parts.form);
        if (action === 'scheduled' && parts.scheduledDateInput && !parts.scheduledDateInput.value) {
            parts.scheduledDateInput.setCustomValidity('Informe a data agendada.');
        } else if (parts.scheduledDateInput) {
            parts.scheduledDateInput.setCustomValidity('');
        }
        if (action === 'paid' && !parts.dateInput.value) {
            parts.dateInput.setCustomValidity('Informe a data de pagamento.');
        } else {
            parts.dateInput.setCustomValidity('');
        }
        if (typeof parts.form.checkValidity === 'function' && !parts.form.checkValidity()) {
            event.preventDefault();
            event.stopPropagation();
            if (action === 'scheduled' && parts.scheduledDateInput && typeof parts.scheduledDateInput.reportValidity === 'function') {
                parts.scheduledDateInput.reportValidity();
            } else if (typeof parts.dateInput.reportValidity === 'function') {
                parts.dateInput.reportValidity();
            }
        }
    }, true);

    // Paid rows still use the legacy HTMX markup without a form payload. Make
    // their confirmed undo explicit without changing the row template: both
    // the status button and the swipe URL receive payment_action=pending.
    document.body.addEventListener('htmx:configRequest', function (event) {
        var detail = event.detail || {};
        var parameters = detail.parameters;
        if (!parameters || parameters.payment_action) return;

        var path = detail.path || (detail.pathInfo && detail.pathInfo.requestPath) || '';
        if (!path || path.indexOf('/status-pagamento') === -1) return;

        var sourceRow = detail.elt && detail.elt.closest ? detail.elt.closest('[data-swipe-right-url]') : null;
        if (!sourceRow) {
            Array.prototype.some.call(document.querySelectorAll('[data-swipe-right-url]'), function (row) {
                if (row.getAttribute('data-swipe-right-url') !== path) return false;
                sourceRow = row;
                return true;
            });
        }
        if (sourceRow && sourceRow.getAttribute('data-swipe-right-url') === path) {
            parameters.payment_action = 'pending';
        }
    });

    document.body.addEventListener('htmx:beforeRequest', function (event) {
        var parts = getModalParts();
        if (!parts.form || event.detail.elt !== parts.form) return;
        parts.form.setAttribute('aria-busy', 'true');
        if (parts.submit) parts.submit.disabled = true;
    });

    document.body.addEventListener('htmx:afterRequest', function (event) {
        var parts = getModalParts();
        if (!parts.form || event.detail.elt !== parts.form) return;
        parts.form.removeAttribute('aria-busy');
        if (parts.submit) parts.submit.disabled = false;
        if (event.detail.successful) window.closePaymentConfirmModal();
    });

    document.body.addEventListener('closePaymentConfirmModal', function () {
        window.closePaymentConfirmModal();
    });

    document.addEventListener('keydown', function (event) {
        if (event.key !== 'Escape') return;
        var parts = getModalParts();
        if (parts.modal && !parts.modal.classList.contains('hidden')) {
            event.stopImmediatePropagation();
            window.closePaymentConfirmModal();
        }
    });
})();
