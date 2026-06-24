// Lancamentos bulk delete guards and filter-safety handlers
// Manages selection state via data-bulk-select container (bulk-select.js).
// This module provides defensive JS handlers independent of component lifecycle.
//
// Proven fixes (keep):
// 1. resetLancamentosBulkState() — multi-level bulk state reset
// 2. Capture-phase submit guard — prevents empty bulk delete form submission
// 3. htmx:beforeSwap — clears state before filter wrapper swap
// 4. htmx:afterSwap — safety reset after filter wrapper swap

(function() {
    'use strict';

    // Multi-level bulk delete state reset.
    // Called before/after filter swaps and from lancamentos-filters change handler.
    // Level 1: DOM-level force hide (style.display + hidden class)
    // Level 2: Clear form inputs + abort pending HTMX requests
    window.resetLancamentosBulkState = function() {
        // Level 1: DOM-level force hide on modal and actions bar
        var modal = document.getElementById('bulk-delete-modal');
        if (modal) {
            modal.style.display = 'none';
            if (!modal.classList.contains('hidden')) {
                modal.classList.add('hidden');
            }
        }

        var bar = document.getElementById('bulk-actions-bar');
        if (bar) {
            bar.style.display = 'none';
            if (!bar.classList.contains('hidden')) {
                bar.classList.add('hidden');
            }
        }

        // Level 2: Clear hidden input containers and abort pending requests
        var ids = ['bulk-delete-inputs', 'bulk-paid-inputs', 'bulk-pending-inputs'];
        for (var i = 0; i < ids.length; i++) {
            var el = document.getElementById(ids[i]);
            if (el) el.innerHTML = '';
        }

        var bulkForm = document.getElementById('bulk-delete-form');
        if (bulkForm && window.htmx) {
            try { window.htmx.trigger(bulkForm, 'htmx:abort'); } catch (ignore) {}
        }
    };

    window.closeLancamentosBulkDeleteModal = window.resetLancamentosBulkState;

    // Capture-phase submit guard — intercepts bulk delete form submission
    // BEFORE HTMX's bubbling-phase listeners, aborting when no ID inputs are present.
    document.body.addEventListener('submit', function(e) {
        var form = e.target && e.target.closest ? e.target.closest('#bulk-delete-form') : null;
        if (!form) return;
        var idInputs = form.querySelectorAll('input[name="ids[]"]');
        if (idInputs.length === 0) {
            e.preventDefault();
            e.stopPropagation();
            return false;
        }
    }, true);

    // Before swap: reset bulk state + clear stale bottom-sheet content.
    document.addEventListener('htmx:beforeSwap', function(event) {
        var target = event.detail && event.detail.target;
        if (!target || target.id !== 'lancamentos-list-wrapper') return;

        if (typeof window.resetLancamentosBulkState === 'function') {
            window.resetLancamentosBulkState();
        }

        var bs = document.getElementById('bottom-sheet-container');
        if (bs && bs.innerHTML.trim() !== '') {
            bs.innerHTML = '';
            if (typeof window._resetOverlaySentinel === 'function') {
                window._resetOverlaySentinel();
            }
        }
    });

    // After swap: safety reset after swap.
    document.addEventListener('htmx:afterSwap', function(event) {
        var target = event.detail.target;
        if (target && target.id === 'lancamentos-list-wrapper') {
            if (typeof window.resetLancamentosBulkState === 'function') {
                window.resetLancamentosBulkState();
            }
        }
    });
})();
