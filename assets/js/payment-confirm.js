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
            submit: document.getElementById('payment-confirm-submit')
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

    function updateSummary() {
        var parts = getModalParts();
        if (!parts.summary || !parts.form) return;
        var action = selectedAction(parts.form);
        if (parts.paidDateRow) parts.paidDateRow.classList.toggle('hidden', action !== 'paid');
        if (parts.scheduledDateRow) parts.scheduledDateRow.classList.toggle('hidden', action !== 'scheduled');
        if (parts.dateInput) parts.dateInput.required = action === 'paid';
        if (parts.scheduledDateInput) parts.scheduledDateInput.required = action === 'scheduled';
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
        parts.summary.textContent = 'Será marcado como pago em ' + formatDateLabel(parts.dateInput && parts.dateInput.value);
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
        if (event.target && (event.target.id === 'payment-confirm-date' || event.target.id === 'payment-scheduled-date' || event.target.hasAttribute('data-payment-mode'))) updateSummary();
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

    document.body.addEventListener('htmx:afterRequest', function (event) {
        var parts = getModalParts();
        if (!parts.form || event.detail.elt !== parts.form || !event.detail.successful) return;
        window.closePaymentConfirmModal();
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
