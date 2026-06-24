// Global Confirmation and Delete Modals
// Extracted from layout.html — Fase 9.9.5e

window._gConfirmState = null;

window.openGlobalConfirm = function(msg, onConfirm, isDestructive, customTitle) {
    var modal = document.getElementById('global-confirm-modal');
    var msgEl = document.getElementById('global-confirm-msg');
    var confirmBtn = document.getElementById('global-confirm-btn');
    var titleEl = document.getElementById('global-confirm-title');
    var iconBg = document.getElementById('global-confirm-icon-bg');
    var icon = document.getElementById('global-confirm-icon');
    
    if (!modal || !msgEl || !confirmBtn) return;

    msgEl.textContent = msg;
    window._gConfirmState = {
        issueRequest: function() { onConfirm(); }
    };

    if (isDestructive) {
        confirmBtn.className = "btn-tactile h-9 rounded-xl bg-rose-600 px-4 text-xs font-semibold text-white shadow-sm hover:bg-rose-700 active:scale-95 transition-all";
        if (titleEl) titleEl.textContent = customTitle || "Confirmar Exclusão";
        if (iconBg) iconBg.className = "flex h-10 w-10 shrink-0 items-center justify-center rounded-full bg-rose-100 dark:bg-rose-500/20 text-rose-600 dark:text-rose-400";
        if (icon) icon.setAttribute('data-lucide', 'alert-triangle');
    } else {
        confirmBtn.className = "btn-tactile h-9 rounded-xl bg-indigo-600 px-4 text-xs font-semibold text-white shadow-sm hover:bg-indigo-700 active:scale-95 transition-all dark:bg-indigo-500";
        if (titleEl) titleEl.textContent = customTitle || "Confirmação";
        if (iconBg) iconBg.className = "flex h-10 w-10 shrink-0 items-center justify-center rounded-full bg-indigo-100 dark:bg-indigo-500/20 text-indigo-600 dark:text-indigo-400";
        if (icon) icon.setAttribute('data-lucide', 'circle-help');
    }
    if (typeof window.refreshIcons === 'function') window.refreshIcons();

    modal.style.display = 'flex';
    if (typeof window.overlayDidOpen === 'function') window.overlayDidOpen('global-confirm');
};

window.submitGlobalConfirm = function() {
    var modal = document.getElementById('global-confirm-modal');
    if (modal) modal.style.display = 'none';
    if (typeof window.overlayDidClose === 'function') window.overlayDidClose();
    if (window._gConfirmState && window._gConfirmState.issueRequest) {
        window._gConfirmState.issueRequest(true);
        window._gConfirmState = null;
    }
};

window.cancelGlobalConfirm = function() {
    var modal = document.getElementById('global-confirm-modal');
    if (modal) modal.style.display = 'none';
    window._gConfirmState = null;
    if (typeof window.overlayDidClose === 'function') window.overlayDidClose();
};

window._gDeleteState = { txId: null };

window.openSwipeDeleteModal = function(txId, row) {
    window._gDeleteState.txId = txId;
    window._gDeleteState.row = row;
    var modal = document.getElementById('global-delete-scope-modal');
    if (modal) {
        modal.querySelectorAll('input[name="gdel_choice"]').forEach(function(r) { r.checked = r.value === 'single'; });
        modal.style.display = 'flex';
        if (typeof window.overlayDidOpen === 'function') window.overlayDidOpen('global-delete-scope');
    }
};

window.handleRowDelete = function(txId, isSeries) {
    if (isSeries) {
        window.openSwipeDeleteModal(txId, null);
    } else {
        window.openGlobalConfirm('Remover este lançamento?', function() {
            htmx.ajax('DELETE', '/transacoes/' + txId + '?escopo=single', {
                target: '#tx-' + txId,
                swap: 'delete'
            });
        }, true);
    }
};

window.deleteTransactionAndRefresh = function(txId, scope) {
    if (scope !== 'single') {
        var filtersForm = document.getElementById('lancamentosFilters');
        if (filtersForm) {
            htmx.ajax('DELETE', '/transacoes/' + txId + '?escopo=' + scope, { swap: 'none' });
            var expectedPath = '/transacoes/' + txId;
            document.body.addEventListener('htmx:afterRequest', function handler(e) {
                if (e.detail.pathInfo.finalPath !== expectedPath) return;
                document.body.removeEventListener('htmx:afterRequest', handler);
                if (e.detail.successful) {
                    var savedReplaceUrl = filtersForm.getAttribute('hx-replace-url');
                    filtersForm.setAttribute('hx-replace-url', 'false');
                    htmx.trigger(filtersForm, 'change');
                    if (savedReplaceUrl) {
                        filtersForm.setAttribute('hx-replace-url', savedReplaceUrl);
                    } else {
                        filtersForm.setAttribute('hx-replace-url', 'true');
                    }
                }
            });
            return;
        }
    }
    htmx.ajax('DELETE', '/transacoes/' + txId + '?escopo=' + scope, {
        target: '#tx-' + txId,
        swap: 'delete'
    });
};

window.submitGlobalDelete = function() {
    var modal = document.getElementById('global-delete-scope-modal');
    var chosen = modal ? modal.querySelector('input[name="gdel_choice"]:checked') : null;
    var scope = chosen ? chosen.value : 'single';
    var txId = window._gDeleteState.txId;
    if (!txId) return;
    modal.style.display = 'none';
    if (typeof window.overlayDidClose === 'function') window.overlayDidClose();
    window.deleteTransactionAndRefresh(txId, scope);
};

window.cancelGlobalDelete = function() {
    var modal = document.getElementById('global-delete-scope-modal');
    if (modal) modal.style.display = 'none';
    window._gDeleteState = { txId: null };
    if (typeof window.overlayDidClose === 'function') window.overlayDidClose();
};

document.body.addEventListener('refreshLancamentosList', function() {
    var filtersForm = document.getElementById('lancamentosFilters');
    if (!filtersForm) return;
    var savedReplaceUrl = filtersForm.getAttribute('hx-replace-url');
    filtersForm.setAttribute('hx-replace-url', 'false');
    htmx.trigger(filtersForm, 'change');
    var onSwap = function() {
        filtersForm.removeEventListener('htmx:afterSwap', onSwap);
        if (savedReplaceUrl) {
            filtersForm.setAttribute('hx-replace-url', savedReplaceUrl);
        } else {
            filtersForm.setAttribute('hx-replace-url', 'true');
        }
    };
    filtersForm.addEventListener('htmx:afterSwap', onSwap);
});
