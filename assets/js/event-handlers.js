// Event Handler Delegation — replaces simple on* inline handlers
// Uses data-* attributes to map actions to global functions

// --- Debug Helper ---
window.isContaBaseDebugUIEnabled = function () {
    try {
        return localStorage.getItem("contabaseDebugUI") === "1" || window.ContaBaseDebugUI === true;
    } catch (e) {
        return false;
    }
};

window.contabaseDebugUI = function () {
    if (window.isContaBaseDebugUIEnabled()) {
        console.log.apply(console, ['[ContaBase:UI]'].concat(Array.prototype.slice.call(arguments)));
    }
};

window.contabaseInspectUI = function () {
    console.log('--- ContaBase UI Inspection ---');
    console.log('body className:', document.body.className);

    var drawer = document.getElementById('workspaceDrawer');
    console.log('#workspaceDrawer classes:', drawer ? drawer.className : 'missing');

    var backdrop = document.getElementById('drawerBackdrop');
    console.log('#drawerBackdrop classes:', backdrop ? backdrop.className : 'missing');

    var overlays = document.querySelectorAll('.fixed, .absolute');
    var visibleOverlays = [];
    overlays.forEach(function (el) {
        var style = window.getComputedStyle(el);
        if (style.display !== 'none' && style.visibility !== 'hidden' && style.opacity !== '0' && style.pointerEvents !== 'none') {
            if (style.zIndex !== 'auto' && parseInt(style.zIndex, 10) > 10) {
                visibleOverlays.push({
                    id: el.id || el.tagName,
                    classes: el.className,
                    zIndex: style.zIndex,
                    pointerEvents: style.pointerEvents
                });
            }
        }
    });
    console.log('Potentially blocking overlays (z-index > 10, visible, pointer-events != none):', visibleOverlays);
};
// --------------------

(function () {
    // Stop propagation handler — replaces inline onclick="event.stopPropagation()"
    // Attached directly to each element to prevent bubbling to parent handlers.
    function attachStopPropagation() {
        document.querySelectorAll('[data-stop-propagation]:not([data-sp-ready])').forEach(function (el) {
            el.dataset.spReady = 'true';
            el.addEventListener('click', function (e) {
                e.stopPropagation();
            });
        });
    }

    document.body.addEventListener('htmx:afterSwap', attachStopPropagation);
    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', attachStopPropagation);
    } else {
        attachStopPropagation();
    }

    // Click handler delegation
    document.addEventListener('click', function (e) {
        var el = e.target.closest('[data-onclick],[data-onclick-ev],[data-onclick-el],[data-onclick-tab],[data-dismiss-modal],[data-onclick-self],[data-picker-select],[data-row-delete]');
        if (!el) return;

        // data-onclick-self="fnName" → only fires when e.target === el (backdrop click pattern)
        if (el.hasAttribute('data-onclick-self')) {
            if (e.target !== el) return;
            var name = el.getAttribute('data-onclick-self');
            var fn = window[name];
            if (typeof fn === 'function') fn(e);
            return;
        }

        // data-onclick="fnName" → calls window.fnName()
        if (el.hasAttribute('data-onclick')) {
            var name = el.getAttribute('data-onclick');
            var fn = window[name];
            window.contabaseDebugUI('data-onclick trigger:', name, '| found fn:', typeof fn === 'function');
            if (typeof fn === 'function') fn();
        }

        // data-onclick-ev="fnName" → calls window.fnName(event)
        if (el.hasAttribute('data-onclick-ev')) {
            var name = el.getAttribute('data-onclick-ev');
            var fn = window[name];
            if (typeof fn === 'function') fn(e);
        }

        // data-onclick-el="method" → calls el.method()
        if (el.hasAttribute('data-onclick-el')) {
            var method = el.getAttribute('data-onclick-el');
            if (typeof el[method] === 'function') el[method]();
        }

        // data-onclick-tab="fnName" data-tab="value" → calls window.fnName('value')
        if (el.hasAttribute('data-onclick-tab')) {
            var name = el.getAttribute('data-onclick-tab');
            var tab = el.getAttribute('data-tab');
            var fn = window[name];
            if (typeof fn === 'function') fn(tab);
        }

        // data-picker-select → modal picker item selection
        if (el.hasAttribute('data-picker-select')) {
            e.stopPropagation();
            if (typeof window.selectPickerItem === 'function') {
                window.selectPickerItem(
                    el.getAttribute('data-picker-id') || '',
                    el.getAttribute('data-picker-name') || '',
                    el.getAttribute('data-picker-icon') || '',
                    el.getAttribute('data-picker-color') || '',
                    (el.closest('[data-picker-type]') || el).getAttribute('data-picker-type') || '',
                    el.getAttribute('data-picker-box-id') || '',
                    el.getAttribute('data-picker-box-reserved') || '0',
                    el.getAttribute('data-picker-box-name') || '',
                    el.getAttribute('data-picker-limit-max') || '0',
                    el.getAttribute('data-picker-limit-spent') || '0'
                );
            }
            var picker = document.getElementById('modalPickerBackdrop');
            if (picker) picker.remove();
            if (typeof window.overlayDidClose === 'function') window.overlayDidClose();
            return;
        }

        // data-row-delete → transaction row delete button
        if (el.hasAttribute('data-row-delete')) {
            var txId = el.getAttribute('data-row-delete');
            var isSeries = el.getAttribute('data-row-is-series') === 'true';
            if (txId && typeof window.handleRowDelete === 'function') {
                window.handleRowDelete(txId, isSeries);
            }
        }

        // data-dismiss-modal="modalId" → hides modal
        if (el.hasAttribute('data-dismiss-modal')) {
            var modalId = el.getAttribute('data-dismiss-modal');
            var modal = document.getElementById(modalId);
            if (modal) modal.style.display = 'none';
        }
    });

    // Input handler delegation
    document.addEventListener('input', function (e) {
        var el = e.target.closest('[data-oninput]');
        if (!el) return;
        var name = el.getAttribute('data-oninput');
        var fn = window[name];
        if (typeof fn === 'function') fn(el);
    });

    // Change handler delegation
    document.addEventListener('change', function (e) {
        var el = e.target.closest('[data-onchange]');
        if (!el) return;
        var name = el.getAttribute('data-onchange');
        var fn = window[name];
        if (typeof fn === 'function') fn(el);
    });

    // Image error fallback — shows data-avatar-text sibling when image fails to load
    document.addEventListener('error', function (e) {
        var img = e.target;
        if (!img || img.tagName !== 'IMG') return;
        if (!img.hasAttribute('data-avatar-fallback')) return;
        img.style.display = 'none';
        var fallback = img.nextElementSibling;
        if (fallback && fallback.hasAttribute('data-avatar-text')) {
            fallback.style.display = '';
        }
    }, true);
    // Navigation helpers
    window.goBack = function () {
        history.back();
    };
})();
