// ContaBase Overlay Stack — Phase D6–D8
// Back button closes overlays on mobile/PWA before navigating.
//
// Contract: any overlay element must have:
//   data-contabase-overlay          — marks element as overlay
//   data-overlay-priority="<number>" — higher = closes first on Back/ESC (default 0)
//   data-overlay-close="<fnName>"   — global function name to call on close (optional)
//   data-overlay-close-method="display|hidden|remove|innerHTML" — fallback close method
//
// Key design principles (D6):
// - ONE sentinel per overlay stack (idempotent overlayDidOpen)
// - _sentinelPushed is the ONLY guard for pushState (no history.state sync)
// - Popstate handler uses DOM detection (getTopOverlay) exclusively
// - HTMX state is preserved in all pushState/replaceState calls
// - resetOverlayStack clears contabaseOverlay from history.state without navigation

(function() {
    'use strict';

    if (window.__contabaseOverlayStackInstalled) return;
    window.__contabaseOverlayStackInstalled = true;

    var _sentinelPushed = false;
    var _ownPopstateExpected = 0;
    var _ownPopstateExpectedTimer = null;
    var _closingViaBack = false;
    var _suppressOpen = false;
    var _registeredOpenOverlayIds = Object.create(null);
    var _lastAction = 'none';
    var _lastOpenSource = 'none';
    var _lastPopstateDecision = 'none';

    var LEGACY_OVERLAYS = [
        {
            id: 'global-confirm-modal',
            priority: 220,
            detect: function() {
                var el = document.getElementById('global-confirm-modal');
                return el && el.style.display !== 'none' && !el.classList.contains('hidden');
            },
            close: function() { if (typeof window.cancelGlobalConfirm === 'function') window.cancelGlobalConfirm(); }
        },
        {
            id: 'global-delete-scope-modal',
            priority: 219,
            detect: function() {
                var el = document.getElementById('global-delete-scope-modal');
                return el && el.style.display !== 'none' && !el.classList.contains('hidden');
            },
            close: function() {
                var el = document.getElementById('global-delete-scope-modal');
                if (el) {
                    el.style.display = 'none';
                    window._gDeleteState = { txId: null };
                }
            }
        },
        {
            id: 'modalPickerBackdrop',
            priority: 120,
            detect: function() { return !!document.getElementById('modalPickerBackdrop'); },
            close: function() { if (typeof window.closePickerModal === 'function') window.closePickerModal(); }
        },
        {
            id: 'contatos-modal',
            priority: 130,
            detect: function() {
                var el = document.getElementById('contatos-modal');
                return el && !el.classList.contains('hidden');
            },
            close: function() { if (typeof window.closeContatosModal === 'function') window.closeContatosModal(); }
        },
        {
            id: 'lancamentosFilterPanel',
            priority: 40,
            detect: function() {
                var el = document.getElementById('lancamentosFilterPanel');
                return el && !el.classList.contains('hidden');
            },
            close: function() {
                var el = document.getElementById('lancamentosFilterPanel');
                if (el) el.classList.add('hidden');
            }
        },
        {
            id: 'formModalBackdrop',
            priority: 110,
            detect: function() {
                var el = document.getElementById('formModalBackdrop');
                return el && !el.classList.contains('hidden');
            },
            close: function() {
                var backdrop = document.getElementById('formModalBackdrop');
                var originModal = document.getElementById('origemModal');
                var categoryModal = document.getElementById('categoriaModal');
                if (backdrop) backdrop.classList.add('hidden');
                if (originModal) originModal.classList.add('hidden');
                if (categoryModal) categoryModal.classList.add('hidden');
            }
        },
        {
            id: 'bottom-sheet-container',
            priority: 10,
            detect: function() {
                var el = document.getElementById('bottom-sheet-container');
                return el && el.innerHTML.trim() !== '';
            },
            close: function() { if (typeof window.closeBottomSheet === 'function') window.closeBottomSheet(); }
        },
        {
            id: 'workspaceDrawer',
            priority: 5,
            detect: function() {
                var el = document.getElementById('workspaceDrawer');
                return el && !el.classList.contains('translate-x-full');
            },
            close: function() { if (typeof window.closeWorkspaceDrawer === 'function') window.closeWorkspaceDrawer(); }
        }
    ];

    function _isVisible(el, method) {
        if (!el) return false;
        switch (method) {
            case 'display':
                if (el.style.display === 'none') return false;
                try {
                    if (window.getComputedStyle(el).display === 'none') return false;
                } catch (ignore) {}
                return true;
            case 'remove': return true;
            case 'innerHTML': return el.innerHTML.trim() !== '';
            case 'translate': return !el.classList.contains('translate-x-full');
            case 'hidden':
            default: {
                // Fase lancamentos-overlay-sentinel-filtro-fantasma:
                // Check both classList AND computed style to avoid false positives
                // from elements hidden via CSS (display:none).
                var hasHiddenClass = el.classList.contains('hidden');
                if (hasHiddenClass) return false;
                try {
                    var computedDisplay = window.getComputedStyle(el).display;
                    if (computedDisplay === 'none') return false;
                } catch (ignore) {}
                return true;
            }
        }
    }

    function _closeEl(el, method, fnName) {
        if (!el) return;
        if (fnName && typeof window[fnName] === 'function') {
            window[fnName]();
            return;
        }
        switch (method) {
            case 'display': el.style.display = 'none'; break;
            case 'remove': el.remove(); break;
            case 'innerHTML': el.innerHTML = ''; break;
            case 'hidden': el.classList.add('hidden'); break;
            default: el.classList.add('hidden'); break;
        }
    }

    function _mergeState(extra) {
        try {
            var current = history.state;
            var merged = {};
            if (current && typeof current === 'object') {
                for (var k in current) {
                    if (current.hasOwnProperty(k)) merged[k] = current[k];
                }
            }
            if (extra) {
                for (var e in extra) {
                    if (extra.hasOwnProperty(e)) merged[e] = extra[e];
                }
            }
            return merged;
        } catch (ex) {
            return extra || {};
        }
    }

    function _replaceRegisteredOpenOverlays(openOverlays) {
        var next = Object.create(null);
        for (var i = 0; i < openOverlays.length; i++) {
            if (openOverlays[i] && openOverlays[i].id) {
                next[openOverlays[i].id] = true;
            }
        }
        _registeredOpenOverlayIds = next;
    }

    function _getAttrOverlays() {
        var els = document.querySelectorAll('[data-contabase-overlay]');
        var result = [];
        for (var i = 0; i < els.length; i++) {
            var el = els[i];
            var id = el.id || ('overlay-' + i);
            var priority = parseInt(el.getAttribute('data-overlay-priority') || '0', 10);
            var method = el.getAttribute('data-overlay-close-method') || 'hidden';
            var fnName = el.getAttribute('data-overlay-close') || '';
            if (_isVisible(el, method)) {
                result.push({
                    id: id,
                    priority: priority,
                    method: method,
                    fnName: fnName,
                    el: el,
                    detect: (function(elem, m) {
                        return function() { return _isVisible(elem, m); };
                    })(el, method),
                    close: (function(elem, m, fn) {
                        return function() { _closeEl(elem, m, fn); };
                    })(el, method, fnName)
                });
            }
        }
        result.sort(function(a, b) { return b.priority - a.priority; });
        return result;
    }

    function getTopOverlay() {
        var allOpen = getAllOpenOverlays();
        return allOpen.length > 0 ? allOpen[0] : null;
    }

    window.hasOpenOverlay = function() {
        return getTopOverlay() !== null;
    };

    window.getTopOverlayId = function() {
        var top = getTopOverlay();
        return top ? top.id : null;
    };

    window.overlayDidOpen = function(source) {
        var allOpen = getAllOpenOverlays();
        var topInfo = allOpen.length > 0 ? allOpen[0] : null;
        var topEl = topInfo ? document.getElementById(topInfo.id) : null;
        var wasRegistered = !!(topInfo && _registeredOpenOverlayIds[topInfo.id]);
        _replaceRegisteredOpenOverlays(allOpen);

        // Fase lancamentos-overlay-sentinel-filtro-fantasma:
        // Instrumentação temporária sob flag contabase_debug_nav para
        // identificar overlay fantasma durante uso de filtros em /lancamentos.
        try {
            if (localStorage.getItem('contabase_debug_nav') === '1') {
                var debugData = {
                    source: source || 'unknown',
                    sentinelPushed: _sentinelPushed,
                    suppressOpen: _suppressOpen,
                    hasOpenOverlay: window.hasOpenOverlay(),
                    openCount: allOpen.length,
                    duplicateOpen: wasRegistered,
                    topOverlay: topInfo ? {
                        id: topInfo.id,
                        priority: topInfo.priority || 0,
                        method: topInfo.method || 'legacy',
                        fnName: topInfo.fnName || ''
                    } : null,
                    topElementState: topEl ? {
                        classList: Array.from(topEl.classList).join(' '),
                        styleDisplay: topEl.style.display,
                        hidden: topEl.classList.contains('hidden'),
                        innerHTMLLength: (topEl.innerHTML || '').length,
                        computedDisplay: (function() {
                            try { return window.getComputedStyle(topEl).display; } catch(e) { return 'error'; }
                        })(),
                        hasAttributeHidden: topEl.hasAttribute('hidden')
                    } : null,
                    allOpenIds: allOpen.map(function(o) { return o.id; }),
                    url: window.location.pathname + window.location.search,
                    historyLength: history.length
                };
                if (topInfo && topInfo.id === 'lancamentosFilterPanel') {
                    debugData.note = 'lancamentosFilterPanel detected as open — check if Mais panel should be closed';
                }
                if (topInfo && topInfo.id === 'bottom-sheet-container') {
                    debugData.note = 'bottom-sheet-container detected as open — check for stale/phantom content';
                }
                console.log('[ContaBase:Overlay:DEBUG] overlayDidOpen', debugData);
            }
        } catch (ignore) {}

        if (_suppressOpen) {
            _lastAction = 'overlayDidOpen-suppressed';
            _lastOpenSource = 'suppressed';
            return;
        }

        if (!topInfo) {
            _lastAction = 'overlayDidOpen-no-overlay-open';
            _lastOpenSource = 'no-overlay';
            return;
        }

        if (wasRegistered) {
            _lastAction = 'overlayDidOpen-duplicate';
            _lastOpenSource = source || topInfo.id;
            return;
        }

        if (_sentinelPushed) {
            _lastAction = 'overlayDidOpen-sentinel-already-pushed';
            _lastOpenSource = source || topInfo.id;
            return;
        }

        try {
            var mergedState = _mergeState({ contabaseOverlay: true });
            history.pushState(mergedState, '', window.location.pathname + window.location.search);
            _sentinelPushed = true;
            _lastAction = 'sentinel-pushed';
            _lastOpenSource = source || topInfo.id;
        } catch (e) {
            _lastAction = 'sentinel-push-failed';
            _lastOpenSource = 'push-failed';
        }
    };

    window.overlayDidClose = function() {
        _replaceRegisteredOpenOverlays(getAllOpenOverlays());

        if (_closingViaBack) {
            _lastAction = 'overlayDidClose-skipped-closingViaBack';
            return;
        }
        if (window.hasOpenOverlay()) {
            _lastAction = 'overlayDidClose-skipped-stillOpen';
            return;
        }
        _consumeSentinel();
    };

    window.closeTopOverlay = function(reason) {
        var top = getTopOverlay();
        if (!top) return false;

        var closedId = top.id;
        top.close();

        _replaceRegisteredOpenOverlays(getAllOpenOverlays());

        if (!getTopOverlay() && _sentinelPushed) {
            _lastAction = 'closeTopOverlay-consume-sentinel';
            _lastOpenSource = reason || closedId;
            _consumeSentinel();
        }

        return true;
    };

    function _consumeSentinel() {
        if (_sentinelPushed) {
            _sentinelPushed = false;
            _ownPopstateExpected++;
            _lastAction = 'sentinel-consumed';

            if (_ownPopstateExpectedTimer) clearTimeout(_ownPopstateExpectedTimer);
            _ownPopstateExpectedTimer = setTimeout(function() {
                if (_ownPopstateExpected > 0) {
                    _ownPopstateExpected = 0;
                    _lastAction = 'ownPopstateExpected-timeout-reset';
                    if (typeof window.contabaseDebugUI === 'function') {
                        window.contabaseDebugUI('overlay-stack: _ownPopstateExpected reset by safety timeout');
                    }
                }
            }, 800);

            history.back();
        } else {
            _lastAction = 'sentinel-consume-skipped-notPushed';
        }
    }

    window.resetOverlayStack = function() {
        _suppressOpen = true;
        _closingViaBack = true;
        var top = getTopOverlay();
        while (top) {
            top.close();
            top = getTopOverlay();
        }
        _closingViaBack = false;
        _sentinelPushed = false;
        _registeredOpenOverlayIds = Object.create(null);

        if (history.state && history.state.contabaseOverlay) {
            try {
                var cleanState = _mergeState({});
                delete cleanState.contabaseOverlay;
                history.replaceState(cleanState, '', window.location.pathname + window.location.search);
            } catch (e) {}
        }

        _suppressOpen = false;
        _lastAction = 'resetOverlayStack';
        _lastOpenSource = 'reset';
    };

    window.overlaySuppressOpen = function(suppress) {
        _suppressOpen = suppress;
        if (suppress) {
            _lastAction = 'suppressOpen-on';
        } else {
            _lastAction = 'suppressOpen-off';
        }
    };

    window._resetOverlaySentinel = function() {
        if (_sentinelPushed) {
            _sentinelPushed = false;
            _lastAction = 'sentinel-reset-external';
        }
        _registeredOpenOverlayIds = Object.create(null);
        if (_ownPopstateExpected > 0) {
            _ownPopstateExpected = 0;
            if (_ownPopstateExpectedTimer) {
                clearTimeout(_ownPopstateExpectedTimer);
                _ownPopstateExpectedTimer = null;
            }
        }
    };

    window.contabaseInspectOverlays = function() {
        var allOpen = getAllOpenOverlays();
        var detected = [];
        for (var i = 0; i < allOpen.length; i++) {
            detected.push({
                id: allOpen[i].id,
                priority: allOpen[i].priority || 0,
                closeStrategy: allOpen[i].fnName || allOpen[i].method || 'legacy'
            });
        }
        var bsEl = document.getElementById('bottom-sheet-container');
        var pickerContainer = document.getElementById('picker-modal-container');
        var result = {
            open: detected,
            registeredOpenIds: Object.keys(_registeredOpenOverlayIds),
            topOverlay: detected.length > 0 ? detected[0].id : null,
            sentinelPushed: _sentinelPushed,
            ownPopstateExpected: _ownPopstateExpected,
            closingViaBack: _closingViaBack,
            suppressOpen: _suppressOpen,
            historyLength: history.length,
            currentURL: window.location.pathname + window.location.search,
            currentStateIsOverlay: !!(history.state && history.state.contabaseOverlay),
            lastAction: _lastAction,
            lastOpenSource: _lastOpenSource,
            lastPopstateDecision: _lastPopstateDecision,
            attrOverlayCount: document.querySelectorAll('[data-contabase-overlay]:not([style*="display: none"]):not(.hidden)').length,
            pickerBackdropExists: !!document.getElementById('modalPickerBackdrop'),
            formModalOpen: (function() {
                var fb = document.getElementById('formModalBackdrop');
                return fb && !fb.classList.contains('hidden');
            })(),
            bottomSheetChildren: bsEl ? bsEl.children.length : 0,
            pickerContainerChildren: pickerContainer ? pickerContainer.children.length : 0
        };
        if (typeof console !== 'undefined') {
            console.log('[ContaBase:Overlay]', result);
        }
        return result;
    };

    function getAllOpenOverlays() {
        var seen = {};
        var result = [];
        var attrOverlays = _getAttrOverlays();
        for (var i = 0; i < attrOverlays.length; i++) {
            if (!seen[attrOverlays[i].id]) {
                result.push(attrOverlays[i]);
                seen[attrOverlays[i].id] = true;
            }
        }
        for (var j = 0; j < LEGACY_OVERLAYS.length; j++) {
            var id = LEGACY_OVERLAYS[j].id;
            if (seen[id]) continue;
            var hasAttr = document.getElementById(id);
            if (hasAttr && hasAttr.hasAttribute('data-contabase-overlay')) continue;
            if (LEGACY_OVERLAYS[j].detect()) {
                result.push({
                    id: id,
                    priority: LEGACY_OVERLAYS[j].priority || 0,
                    detect: LEGACY_OVERLAYS[j].detect,
                    close: LEGACY_OVERLAYS[j].close
                });
                seen[id] = true;
            }
        }
        result.sort(function(a, b) {
            return (b.priority || 0) - (a.priority || 0);
        });
        return result;
    }

    window.addEventListener('popstate', function(e) {
        if (_ownPopstateExpected > 0) {
            _ownPopstateExpected--;
            if (_ownPopstateExpectedTimer) {
                clearTimeout(_ownPopstateExpectedTimer);
                _ownPopstateExpectedTimer = null;
            }
            _lastAction = 'popstate-ownExpected';
            _lastPopstateDecision = 'own-expected-consumed';
            if (typeof window.updateFAB === 'function') window.updateFAB();
            e.stopImmediatePropagation();
            e.preventDefault();
            return;
        }

        var top = getTopOverlay();
        if (!top) {
            _lastAction = 'popstate-noOverlay';
            _lastPopstateDecision = 'no-overlay-pass-through';
            return;
        }

        _closingViaBack = true;
        var overlayId = top.id;
        top.close();
        _closingViaBack = false;

        _sentinelPushed = false;
        _replaceRegisteredOpenOverlays(getAllOpenOverlays());

        var stillOpen = getTopOverlay();
        if (stillOpen) {
            try {
                var mergedState = _mergeState({ contabaseOverlay: true });
                history.pushState(mergedState, '', window.location.pathname + window.location.search);
                _sentinelPushed = true;
                _lastAction = 'popstate-closed-repushed';
                _lastPopstateDecision = 'closed-' + overlayId + '-repushed-' + stillOpen.id;
            } catch (ex) {
                _lastAction = 'popstate-closed-repush-failed';
                _lastPopstateDecision = 'closed-' + overlayId + '-repush-failed';
            }
        } else {
            _lastAction = 'popstate-closed-noMoreOverlays';
            _lastPopstateDecision = 'closed-' + overlayId + '-no-more-overlays';
        }

        if (typeof window.updateFAB === 'function') window.updateFAB();

        e.stopImmediatePropagation();
        e.preventDefault();
    }, true);

    document.body.addEventListener('htmx:afterSwap', function(e) {
        var target = e.detail && e.detail.target;
        if (_suppressOpen) return;
        if (target && target.id === 'bottom-sheet-container' && target.innerHTML.trim() !== '') {
            // Fase lancamentos-overlay-sentinel-filtro-fantasma:
            // Instrumentação sob flag contabase_debug_nav para identificar
            // se bottom-sheet-container está sendo erroneamente detectado como aberto.
            try {
                if (localStorage.getItem('contabase_debug_nav') === '1') {
                    var requestPath = e.detail && e.detail.pathInfo ? e.detail.pathInfo.requestPath : 'unknown';
                    console.log('[ContaBase:Overlay:DEBUG] htmx:afterSwap overlayDidOpen trigger', {
                        targetId: target.id,
                        innerHTMLLength: target.innerHTML.length,
                        innerHTMLPreview: (target.innerHTML || '').substring(0, 200),
                        requestPath: requestPath,
                        swapStyle: e.detail.swapStyle,
                        url: window.location.pathname + window.location.search,
                        sentinelPushed: _sentinelPushed
                    });
                }
            } catch (ignore) {}
            window.overlayDidOpen('htmx:afterSwap(bottom-sheet-container)');
        }
    });

})();
