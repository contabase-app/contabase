// HTMX Event Handlers — replaces hx-on:* inline attributes
// Uses data-hx-after on form elements to define post-request behavior

(function() {
    document.body.addEventListener('htmx:afterRequest', function(e) {
        if (!e.detail.successful) return;
        var el = e.detail.elt;

        if (el && el.hasAttribute('data-hx-after')) {
            var action = el.getAttribute('data-hx-after');

            switch (action) {
                case 'closeBottomSheet':
                    if (typeof closeBottomSheet === 'function') closeBottomSheet();
                    break;

                case 'closeContatosModal':
                    if (typeof closeContatosModal === 'function') closeContatosModal();
                    if (el.tagName === 'FORM') el.reset();
                    break;

                case 'resetBulkSelection':
                case 'resetBulkSelectionDel':
                    var bulkContainers = document.querySelectorAll('[data-bulk-select]');
                    for (var b = 0; b < bulkContainers.length; b++) {
                        bulkContainers[b].dispatchEvent(new CustomEvent('contabase:bulk-reset', { bubbles: false }));
                    }
                    break;
            }
        }
    });
})();
