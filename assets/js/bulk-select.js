/**
 * ContaBase Bulk Select — CSP-compliant (zero inline handlers)
 * 
 * Substitui Alpine.js para selecao em lote usando data attributes
 * e event delegation. Totalmente compativel com Content Security Policy.
 */
(function () {
    'use strict';

    window.lancamentosHasSeries = function (ids) {
        for (var i = 0; i < ids.length; i++) {
            var el = document.querySelector('[data-tx-id="' + ids[i] + '"]');
            if (el && el.dataset.isSeries === 'true') return true;
        }
        return false;
    };

    function initBulkSelect(container) {
        if (container.dataset.bulkReady === 'true') return;
        container.dataset.bulkReady = 'true';

        var state = {
            mode: false,
            ids: [],
            longPressTimer: null,
            longPressTriggered: false,
            deleteModalOpen: false,
            deleteChoice: 'selected_only',
            deleteIDs: [],
            deleteConfirmed: false,
            deleteSubmitting: false,
            deleteFailed: false,
            isTouch: (window.matchMedia && window.matchMedia('(pointer: coarse)').matches) || ('ontouchstart' in window)
        };

        var dom = {
            toggleBtn: container.querySelector('[data-bulk-toggle-mode]'),
            toggleAll: container.querySelector('[data-bulk-toggle-all]'),
            countLabel: container.querySelector('[data-bulk-count]'),
            countNum: container.querySelector('[data-bulk-count-num]'),
            modalCount: container.querySelector('[data-bulk-modal-count]'),
            actionBar: container.querySelector('[data-bulk-bar]'),
            modal: container.querySelector('[data-bulk-modal]'),
            deleteBtn: container.querySelector('[data-bulk-delete-btn]'),
            deleteForm: container.querySelector('[data-bulk-delete-form]'),
            deleteScopeInput: container.querySelector('[data-bulk-delete-scope]'),
            deleteConfirm: container.querySelector('[data-action="confirm-delete"]'),
            deleteError: container.querySelector('[data-bulk-delete-error]'),
            formsPaid: container.querySelector('[data-bulk-form-ids][data-action="paid"]'),
            formsPending: container.querySelector('[data-bulk-form-ids][data-action="pending"]'),
            formsDelete: container.querySelector('[data-bulk-form-ids][data-action="delete"]')
        };

        function syncDOM() {
            container.classList.toggle('bulk-mode', state.mode);
            var activeBulkSelection = state.mode && state.ids.length > 0;
            if (activeBulkSelection) {
                container.setAttribute('data-bulk-active', 'true');
            } else {
                container.removeAttribute('data-bulk-active');
            }
            document.body.classList.toggle('has-bulk-selection', !!document.querySelector('[data-bulk-select][data-bulk-active="true"]'));
            if (typeof window.updateFAB === 'function') window.updateFAB();
            if (dom.toggleBtn) {
                dom.toggleBtn.textContent = state.mode ? 'Cancelar' : 'Selecionar';
            }
            if (dom.countLabel) {
                dom.countLabel.textContent = state.ids.length ? state.ids.length + ' selecionado(s)' : '';
            }
            if (dom.countNum) {
                dom.countNum.textContent = state.ids.length;
            }
            if (dom.modalCount) {
                dom.modalCount.textContent = state.deleteModalOpen ? state.deleteIDs.length : state.ids.length;
            }
            if (dom.actionBar) {
                dom.actionBar.classList.toggle('hidden', !activeBulkSelection);
            }
            var checkboxes = container.querySelectorAll('[data-tx-checkbox]');
            checkboxes.forEach(function (cb) {
                cb.checked = state.ids.indexOf(cb.value) !== -1;
            });
            if (dom.toggleAll) {
                dom.toggleAll.checked = state.ids.length > 0 && checkboxes.length > 0 && state.ids.length === checkboxes.length;
            }
        }

        function updateForms() {
            container.querySelectorAll('[data-bulk-id-input]').forEach(function(el) { el.remove(); });
            [dom.formsPaid, dom.formsPending, dom.formsDelete].forEach(function (input) {
                if (input) {
                    var form = input.closest('form');
                    if (form) {
                        state.ids.forEach(function(id) {
                            var hidden = document.createElement('input');
                            hidden.type = 'hidden';
                            hidden.name = 'ids[]';
                            hidden.value = id;
                            hidden.setAttribute('data-bulk-id-input', '');
                            form.appendChild(hidden);
                        });
                    }
                }
            });
            if (dom.deleteScopeInput) {
                dom.deleteScopeInput.value = state.deleteChoice;
            }
        }

        function toggleMode(on) {
            state.mode = typeof on === 'boolean' ? on : !state.mode;
            if (!state.mode) state.ids = [];
            syncDOM();
        }

        function toggleId(id) {
            var idx = state.ids.indexOf(id);
            if (idx === -1) {
                state.ids.push(id);
            } else {
                state.ids.splice(idx, 1);
            }
            syncDOM();
        }

        function toggleAll() {
            if (!state.mode) { state.mode = true; }
            var checkboxes = container.querySelectorAll('[data-tx-checkbox]');
            var allChecked = checkboxes.length > 0 && state.ids.length === checkboxes.length;
            if (allChecked) {
                state.ids = [];
            } else {
                state.ids = [];
                checkboxes.forEach(function (cb) { state.ids.push(cb.value); });
            }
            syncDOM();
        }

        function openDeleteModal() {
            if (!dom.modal || !state.ids.length || state.deleteSubmitting || state.deleteModalOpen) return;
            state.deleteIDs = state.ids.slice();
            state.deleteChoice = 'selected_only';
            state.deleteModalOpen = true;
            var hasSeries = window.lancamentosHasSeries(state.deleteIDs);
            dom.modal.querySelector('[data-bulk-delete-title]').textContent = hasSeries ? 'Excluir série?' : 'Excluir lançamentos?';
            dom.modal.querySelector('[data-bulk-delete-description]').textContent = hasSeries
                ? 'Escolha se a exclusão atinge apenas os lançamentos marcados ou também as ocorrências futuras da série.'
                : 'Confirme a exclusão dos lançamentos marcados.';
            dom.modal.querySelector('[data-bulk-delete-scopes]').classList.toggle('hidden', !hasSeries);
            dom.modal.querySelectorAll('[data-bulk-delete-choice]').forEach(function (radio) {
                radio.checked = radio.value === 'selected_only';
                radio.disabled = !hasSeries;
            });
            dom.deleteConfirm.disabled = state.deleteFailed;
            syncDOM();
            dom.modal.classList.remove('hidden');
            dom.modal.querySelector('[data-action="cancel-delete"]').focus();
        }

        function closeDeleteModal() {
            if (!state.deleteModalOpen || state.deleteSubmitting) return;
            state.deleteModalOpen = false;
            state.deleteConfirmed = false;
            state.deleteIDs = [];
            if (dom.modal) dom.modal.classList.add('hidden');
            if (dom.deleteBtn) dom.deleteBtn.focus();
        }

        function deleteSelectionMatches() {
            var checked = Array.prototype.filter.call(container.querySelectorAll('[data-tx-checkbox]'), function (cb) { return cb.checked; });
            return state.deleteIDs.length > 0 && state.ids.length === state.deleteIDs.length && checked.length === state.deleteIDs.length &&
                state.deleteIDs.every(function (id) {
                    return state.ids.indexOf(id) !== -1 && checked.some(function (cb) { return cb.value === id; });
                });
        }

        function deleteFailed() {
            state.deleteSubmitting = false;
            state.deleteConfirmed = false;
            state.deleteFailed = true;
            dom.deleteForm.removeAttribute('aria-busy');
            if (document.activeElement === dom.deleteConfirm) dom.modal.querySelector('[role="dialog"]').focus();
            dom.deleteConfirm.disabled = true;
            dom.deleteConfirm.textContent = 'Confirmar exclusão';
            // A failed response can follow a committed delete. Never retry it
            // against a stale selection or claim that nothing was removed.
            dom.deleteError.textContent = 'Não foi possível confirmar a exclusão. Recarregue a lista antes de tentar novamente.';
            dom.deleteError.classList.remove('hidden');
        }

        function submitDeleteForm() {
            if (!state.deleteModalOpen || state.deleteSubmitting || state.deleteFailed) return;
            if (!deleteSelectionMatches()) {
                closeDeleteModal();
                return;
            }
            updateForms();
            state.deleteConfirmed = true;
            try {
                dom.deleteForm.requestSubmit();
            } catch (error) {
                deleteFailed();
            }
        }

        if (dom.deleteForm) {
            // Capture before HTMX's submit listener consumes the request.
            dom.deleteForm.addEventListener('submit', function (event) {
                if (!state.deleteConfirmed || state.deleteSubmitting || !deleteSelectionMatches()) {
                    event.preventDefault();
                    event.stopImmediatePropagation();
                    return;
                }
                state.deleteConfirmed = false;
                state.deleteSubmitting = true;
                dom.deleteForm.setAttribute('aria-busy', 'true');
                if (document.activeElement === dom.deleteConfirm) dom.modal.querySelector('[role="dialog"]').focus();
                dom.deleteConfirm.disabled = true;
                dom.deleteConfirm.textContent = 'Excluindo…';
            }, true);
            dom.deleteForm.addEventListener('htmx:afterRequest', function (event) {
                if (event.detail.elt !== dom.deleteForm) return;
                if (!event.detail.successful) {
                    deleteFailed();
                    return;
                }
                state.deleteSubmitting = false;
                dom.deleteForm.removeAttribute('aria-busy');
                dom.deleteConfirm.textContent = 'Confirmar exclusão';
                closeDeleteModal();
            });
        }

        function clearLongPress() {
            if (state.longPressTimer) { clearTimeout(state.longPressTimer); state.longPressTimer = null; }
        }

        // Click delegation
        container.addEventListener('click', function (event) {
            var target = event.target;

            // Transaction row checkbox — must be checked BEFORE data-ignore-select guard
            // because checkboxes carry data-ignore-select to prevent HTMX navigation
            var checkbox = target.closest('[data-tx-checkbox]');
            if (checkbox && state.mode) {
                toggleId(checkbox.value);
                return;
            }

            // Ignore clicks on ignore-select elements (except checkboxes handled above)
            if (target.closest('[data-ignore-select]')) return;

            // Toggle mode button
            if (target.closest('[data-bulk-toggle-mode]')) {
                toggleMode();
                return;
            }

            // Toggle all
            if (target.closest('[data-bulk-toggle-all]')) {
                toggleAll();
                return;
            }

            // Bulk action buttons (these have their own form/HTMX handlers, just update IDs first)
            if (target.closest('[data-bulk-mark-paid]')) {
                event.preventDefault();
                event.stopPropagation();
                updateForms();
                if (typeof window.openBulkPaymentConfirmModal === 'function') {
                    var paidForm = target.closest('form');
                    window.openBulkPaymentConfirmModal({
                        ids: state.ids.slice(),
                        postURL: paidForm ? paidForm.getAttribute('hx-post') : '/transacoes/bulk/update',
                        target: paidForm ? paidForm.getAttribute('hx-target') : '#lancamentos-table-body',
                        swap: paidForm ? paidForm.getAttribute('hx-swap') : 'outerHTML'
                    });
                }
                return;
            }
            if (target.closest('[data-bulk-mark-pending]')) {
                updateForms();
                return;
            }
            if (target.closest('[data-bulk-delete-btn]')) {
                updateForms();
                openDeleteModal();
                return;
            }

            // Delete modal: close on backdrop
            if (target === dom.modal) {
                closeDeleteModal();
                return;
            }

            // Delete modal buttons
            if (target.closest('[data-action="cancel-delete"]')) {
                closeDeleteModal();
                return;
            }
            if (target.closest('[data-action="confirm-delete"]')) {
                submitDeleteForm();
                return;
            }

            // Delete scope radio
            var scopeRadio = target.closest('[data-bulk-delete-choice]');
            if (scopeRadio) {
                if (!state.deleteModalOpen || state.deleteSubmitting || scopeRadio.disabled) return;
                state.deleteChoice = scopeRadio.value === 'with_future' ? 'with_future' : 'selected_only';
                updateForms();
                return;
            }

            // Transaction row click (in selection mode)
            var row = target.closest('[data-swipe-content]');
            if (row && state.mode) {
                event.preventDefault();
                event.stopPropagation();
                var rowCheckbox = row.querySelector('[data-tx-checkbox]');
                if (rowCheckbox) toggleId(rowCheckbox.value);
                return;
            }
        });

        container.addEventListener('keydown', function (event) {
            if (!state.deleteModalOpen) return;
            if (event.key === 'Escape') {
                event.preventDefault();
                closeDeleteModal();
            } else if (event.key === 'Tab') {
                var controls = Array.prototype.filter.call(dom.modal.querySelectorAll('button, input'), function (el) {
                    if (el.matches(':disabled') || el.tabIndex < 0 || el.closest('.hidden, [hidden], [inert]') || !el.getClientRects().length) return false;
                    var visibility = window.getComputedStyle(el).visibility;
                    return visibility !== 'hidden' && visibility !== 'collapse';
                });
                // A named radio group contributes one Tab stop, even when its
                // checked member is not the first input in DOM order.
                controls = controls.filter(function (el, index, candidates) {
                    if (el.type !== 'radio' || !el.name) return true;
                    var group = candidates.filter(function (other) {
                        return other.type === 'radio' && other.name === el.name && other.form === el.form;
                    });
                    return el === (group.filter(function (radio) { return radio.checked; })[0] || group[0]);
                }).sort(function (a, b) {
                    return (a.tabIndex || Infinity) - (b.tabIndex || Infinity);
                });
                var first = controls[0];
                var last = controls[controls.length - 1];
                if (!first) {
                    event.preventDefault();
                    dom.modal.querySelector('[role="dialog"]').focus();
                } else if (controls.indexOf(document.activeElement) === -1) {
                    event.preventDefault();
                    (event.shiftKey ? last : first).focus();
                } else if (event.shiftKey && document.activeElement === first) {
                    event.preventDefault();
                    last.focus();
                } else if (!event.shiftKey && document.activeElement === last) {
                    event.preventDefault();
                    first.focus();
                }
            }
        });

        // Submit interception: populate hidden inputs before submit
        container.addEventListener('submit', function (event) {
            var form = event.target.closest('form');
            if (!form) return;
            if (form.querySelector('[data-bulk-form-ids]')) {
                updateForms();
            }
        });

        // External reset trigger (e.g. from hx-events after successful bulk action)
        container.addEventListener('contabase:bulk-reset', function () {
            closeDeleteModal();
            state.mode = false;
            state.ids = [];
            syncDOM();
        });

        // Touch: long press to enter selection mode
        container.addEventListener('touchstart', function (event) {
            if (!state.isTouch || state.mode) return;
            var row = event.target.closest('[data-swipe-content]');
            if (!row) return;
            if (event.target.closest('[data-ignore-select]')) return;
            var checkbox = row.querySelector('[data-tx-checkbox]');
            if (!checkbox) return;
            var id = checkbox.value;
            state.longPressTriggered = false;
            clearLongPress();
            state.longPressTimer = setTimeout(function () {
                state.mode = true;
                state.ids = [id];
                state.longPressTriggered = true;
                syncDOM();
                if (navigator.vibrate) navigator.vibrate(15);
            }, 500);
        }, { passive: true });

        container.addEventListener('touchend', function () { clearLongPress(); });
        container.addEventListener('touchcancel', function () { clearLongPress(); });
        container.addEventListener('touchmove', function () { clearLongPress(); });

        // Init
        syncDOM();
    }

    function initAll() {
        document.querySelectorAll('[data-bulk-select]').forEach(initBulkSelect);
    }

    // Run on load
    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', initAll);
    } else {
        initAll();
    }

    // Re-run after HTMX swaps
    if (!window.__contabaseBulkSelectSettleListener) {
        window.__contabaseBulkSelectSettleListener = true;
        document.addEventListener('htmx:afterSettle', function (event) {
            var target = event.detail.target || event.target;
            if (target.querySelectorAll) {
                target.querySelectorAll('[data-bulk-select]').forEach(initBulkSelect);
            }
            if (target.hasAttribute && target.hasAttribute('data-bulk-select')) {
                initBulkSelect(target);
            }
            document.querySelectorAll('[data-bulk-select]:not([data-bulk-ready])').forEach(initBulkSelect);
        });
    }

})();
