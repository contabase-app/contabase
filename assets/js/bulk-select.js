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
            isTouch: (window.matchMedia && window.matchMedia('(pointer: coarse)').matches) || ('ontouchstart' in window)
        };

        var dom = {
            toggleBtn: container.querySelector('[data-bulk-toggle-mode]'),
            toggleAll: container.querySelector('[data-bulk-toggle-all]'),
            countLabel: container.querySelector('[data-bulk-count]'),
            countNum: container.querySelector('[data-bulk-count-num]'),
            actionBar: container.querySelector('[data-bulk-bar]'),
            modal: container.querySelector('[data-bulk-modal]'),
            deleteForm: container.querySelector('[data-bulk-delete-form]'),
            deleteScopeInput: container.querySelector('[data-bulk-delete-scope]'),
            formsPaid: container.querySelector('[data-bulk-form-ids][data-action="paid"]'),
            formsPending: container.querySelector('[data-bulk-form-ids][data-action="pending"]'),
            formsDelete: container.querySelector('[data-bulk-form-ids][data-action="delete"]')
        };

        function syncDOM() {
            container.classList.toggle('bulk-mode', state.mode);
            if (dom.toggleBtn) {
                dom.toggleBtn.textContent = state.mode ? 'Cancelar' : 'Selecionar';
            }
            if (dom.countLabel) {
                dom.countLabel.textContent = state.ids.length ? state.ids.length + ' selecionado(s)' : '';
            }
            if (dom.countNum) {
                dom.countNum.textContent = state.ids.length;
            }
            if (dom.actionBar) {
                dom.actionBar.classList.toggle('hidden', !(state.mode && state.ids.length > 0));
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
            if (state.ids.length === 0) return;
            if (window.lancamentosHasSeries && window.lancamentosHasSeries(state.ids)) {
                state.deleteModalOpen = true;
                if (dom.modal) dom.modal.classList.remove('hidden');
            } else {
                state.deleteChoice = 'selected_only';
                updateForms();
                submitDeleteForm();
            }
        }

        function submitDeleteForm() {
            state.deleteModalOpen = false;
            if (dom.modal) dom.modal.classList.add('hidden');
            updateForms();
            if (dom.deleteForm) dom.deleteForm.requestSubmit();
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
                updateForms();
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
                state.deleteModalOpen = false;
                dom.modal.classList.add('hidden');
                return;
            }

            // Delete modal buttons
            if (target.closest('[data-action="cancel-delete"]')) {
                state.deleteModalOpen = false;
                dom.modal.classList.add('hidden');
                return;
            }
            if (target.closest('[data-action="confirm-delete"]')) {
                submitDeleteForm();
                return;
            }

            // Delete scope radio
            var scopeRadio = target.closest('[data-bulk-delete-choice]');
            if (scopeRadio) {
                state.deleteChoice = scopeRadio.value;
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
