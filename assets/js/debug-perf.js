// ContaBase Debug Performance Instrumentation
// Fase A + extensões D8 — Apenas medicao, sem alterar comportamento
// Ativacao: localStorage.setItem('contabase_debug_nav', '1')
// Desativacao: localStorage.removeItem('contabase_debug_nav')

(function() {
    'use strict';

    if (window.__contabaseDebugPerfInstalled) return;
    window.__contabaseDebugPerfInstalled = true;

    var FLAG_KEY = 'contabase_debug_nav';
    var isActive = false;

    function checkFlag() {
        try {
            isActive = localStorage.getItem(FLAG_KEY) === '1';
        } catch (e) {
            isActive = false;
        }
        return isActive;
    }

    if (!checkFlag()) return;

    var counters = {
        historyPush: 0,
        historyReplace: 0,
        popstate: 0,
        htmxBeforeSwap: 0,
        htmxAfterSwap: 0,
        htmxAfterSettle: 0,
        htmxHistoryRestore: 0,
        htmxConfigRequest: 0,
        initAppShellTotal: 0,
        initAppShellDocument: 0,
        initAppShellRoot: 0,
        lucideCreateIcons: 0
    };

    var timestamps = {};
    var requestStarts = {};
    var logs = [];

    function log(level, message, data) {
        if (!isActive) return;
        var entry = {
            time: new Date().toISOString(),
            level: level,
            message: message,
            data: data || null
        };
        logs.push(entry);
        if (logs.length > 500) logs.shift();
        var prefix = '[ContaBase:Debug] ';
        if (level === 'warn') {
            console.warn(prefix + message, data || '');
        } else if (level === 'error') {
            console.error(prefix + message, data || '');
        } else {
            console.log(prefix + message, data || '');
        }
    }

    function isElRendered(el) {
        if (!el) return false;
        var style = window.getComputedStyle(el);
        return style.display !== 'none' && style.visibility !== 'hidden';
    }

    function getUIState() {
        var state = {
            bottomSheet: false,
            workspaceDrawer: false,
            confirmModal: false,
            deleteModal: false,
            filterPanel: false,
            pickerModal: false,
            formModal: false,
            contatosModal: false
        };
        try {
            var bs = document.getElementById('bottom-sheet-container');
            if (bs && bs.innerHTML.trim() !== '') state.bottomSheet = true;

            var drawer = document.getElementById('workspaceDrawer');
            if (drawer && !drawer.classList.contains('translate-x-full')) state.workspaceDrawer = true;

            var confirm = document.getElementById('global-confirm-modal');
            if (confirm && isElRendered(confirm)) state.confirmModal = true;

            var del = document.getElementById('global-delete-scope-modal');
            if (del && isElRendered(del)) state.deleteModal = true;

            var filter = document.getElementById('lancamentosFilterPanel');
            if (filter && !filter.classList.contains('hidden')) state.filterPanel = true;

            var picker = document.getElementById('modalPickerBackdrop');
            if (picker) state.pickerModal = true;

            var fmb = document.getElementById('formModalBackdrop');
            if (fmb && !fmb.classList.contains('hidden')) state.formModal = true;

            var cm = document.getElementById('contatos-modal');
            if (cm && !cm.classList.contains('hidden')) state.contatosModal = true;
        } catch (e) {
            state.error = e.message;
        }
        return state;
    }

    function getTriggerElementInfo(elt) {
        if (!elt) return null;
        var info = {
            tag: elt.tagName,
            id: elt.id || null,
            className: (elt.className || '').substring(0, 80),
            hxGet: elt.getAttribute('hx-get') || null,
            hxPost: elt.getAttribute('hx-post') || null,
            hxPushUrl: elt.getAttribute('hx-push-url') || null,
            hxReplaceUrl: elt.getAttribute('hx-replace-url') || null,
            hxTarget: elt.getAttribute('hx-target') || null,
            hxSwap: elt.getAttribute('hx-swap') || null,
            hxBoost: elt.getAttribute('hx-boost') || null,
            dataOnclick: elt.getAttribute('data-onclick') || null,
            dataOnclickEv: elt.getAttribute('data-onclick-ev') || null,
            outerHTML: (elt.outerHTML || '').substring(0, 200)
        };
        var ancestry = [];
        var parent = elt.parentElement;
        for (var i = 0; i < 5 && parent; i++) {
            ancestry.push({
                tag: parent.tagName,
                id: parent.id || null,
                className: (parent.className || '').substring(0, 60),
                hxPushUrl: parent.getAttribute('hx-push-url') || null,
                hxReplaceUrl: parent.getAttribute('hx-replace-url') || null,
                hxBoost: parent.getAttribute('hx-boost') || null,
                hxTarget: parent.getAttribute('hx-target') || null
            });
            parent = parent.parentElement;
        }
        info.ancestry = ancestry;
        return info;
    }

    function wrapHistoryPush() {
        if (history.__contabasePushWrapped) return;
        var orig = history.pushState;
        history.__contabasePushWrapped = true;
        history.__contabasePushOriginal = orig;
        history.pushState = function(state, title, url) {
            if (!isActive) return history.__contabasePushOriginal.apply(this, arguments);
            counters.historyPush++;
            var data = {
                state: state,
                url: url || window.location.href,
                historyLength: history.length,
                origin: 'unknown'
            };
            try {
                if (state && state.contabaseOverlay === true) {
                    data.origin = 'overlay-sentinel';
                } else if (state && state.htmx) {
                    data.origin = 'htmx';
                } else if (state === null) {
                    data.origin = 'null-orphan';
                } else {
                    data.origin = 'custom';
                }
            } catch (e) {}
            log('info', 'history.pushState', data);
            return history.__contabasePushOriginal.apply(this, arguments);
        };
    }

    function wrapHistoryReplace() {
        if (history.__contabaseReplaceWrapped) return;
        var orig = history.replaceState;
        history.__contabaseReplaceWrapped = true;
        history.__contabaseReplaceOriginal = orig;
        history.replaceState = function(state, title, url) {
            if (!isActive) return history.__contabaseReplaceOriginal.apply(this, arguments);
            counters.historyReplace++;
            var data = {
                state: state,
                url: url || window.location.href,
                historyLength: history.length,
                origin: 'unknown'
            };
            try {
                if (state && state.contabaseOverlay === true) {
                    data.origin = 'overlay-sentinel-replace';
                } else if (state && state.htmx) {
                    data.origin = 'htmx';
                } else {
                    data.origin = 'custom';
                }
            } catch (e) {}
            log('info', 'history.replaceState', data);
            return history.__contabaseReplaceOriginal.apply(this, arguments);
        };
    }

    function wrapInitAppShell() {
        if (window.__contabaseInitAppShellWrapped) return;
        if (typeof window.initAppShell !== 'function') return;
        var orig = window.initAppShell;
        window.__contabaseInitAppShellWrapped = true;
        window.__contabaseInitAppShellOriginal = orig;
        window.initAppShell = function(root) {
            if (!isActive) return window.__contabaseInitAppShellOriginal.apply(this, arguments);
            counters.initAppShellTotal++;
            var isDocument = (root === document || root === document.documentElement);
            if (isDocument) {
                counters.initAppShellDocument++;
            } else {
                counters.initAppShellRoot++;
            }
            var data = {
                rootType: isDocument ? 'document' : (root ? root.id || root.tagName || 'element' : 'null'),
                isDocument: isDocument,
                callCount: counters.initAppShellTotal
            };
            if (isDocument) {
                log('warn', 'initAppShell(document) — DOM inteiro', data);
            } else {
                log('info', 'initAppShell', data);
            }
            return window.__contabaseInitAppShellOriginal.apply(this, arguments);
        };
    }

    function wrapLucideCreateIcons() {
        if (window.__contabaseLucideWrapped) return;
        if (!window.lucide || typeof window.lucide.createIcons !== 'function') return;
        var orig = window.lucide.createIcons;
        window.__contabaseLucideWrapped = true;
        window.__contabaseLucideOriginal = orig;
        window.lucide.createIcons = function() {
            if (!isActive) return window.__contabaseLucideOriginal.apply(this, arguments);
            counters.lucideCreateIcons++;
            var start = performance.now();
            var result = window.__contabaseLucideOriginal.apply(this, arguments);
            var duration = performance.now() - start;
            var iconCount = 0;
            try {
                iconCount = document.querySelectorAll('[data-lucide]').length;
            } catch (e) {}
            var data = {
                duration: duration.toFixed(2) + 'ms',
                iconCount: iconCount,
                callCount: counters.lucideCreateIcons
            };
            log('info', 'lucide.createIcons', data);
            return result;
        };
    }

    wrapHistoryPush();
    wrapHistoryReplace();
    wrapInitAppShell();
    wrapLucideCreateIcons();

    document.body.addEventListener('htmx:configRequest', function(e) {
        if (!isActive) return;
        counters.htmxConfigRequest++;
        var detail = e.detail || {};
        var elt = detail.elt;
        var path = detail.path || '';
        var data = {
            path: path,
            target: detail.target ? (detail.target.id || detail.target.tagName) : null,
            verb: detail.verb || null,
            triggerInfo: getTriggerElementInfo(elt)
        };
        requestStarts[path] = { start: performance.now(), verb: detail.verb || 'GET' };
        log('info', 'htmx:configRequest', data);
    });

    document.body.addEventListener('htmx:beforeSwap', function(e) {
        if (!isActive) return;
        counters.htmxBeforeSwap++;
        var detail = e.detail || {};
        var targetId = detail.target ? (detail.target.id || detail.target.tagName) : 'no-target';
        var requestPath = detail.pathInfo ? detail.pathInfo.requestPath : '';
        var data = {
            target: targetId,
            swapStyle: detail.swapStyle,
            pathInfo: requestPath
        };
        timestamps['swap_' + requestPath + '_' + targetId] = performance.now();
        log('info', 'htmx:beforeSwap', data);
    });

    document.body.addEventListener('htmx:afterSwap', function(e) {
        if (!isActive) return;
        counters.htmxAfterSwap++;
        var detail = e.detail || {};
        var targetId = detail.target ? (detail.target.id || detail.target.tagName) : 'no-target';
        var requestPath = detail.pathInfo ? detail.pathInfo.requestPath : '';
        var data = {
            target: targetId,
            swapStyle: detail.swapStyle,
            pathInfo: requestPath
        };
        var swapKey = 'swap_' + requestPath + '_' + targetId;
        if (timestamps[swapKey]) {
            data.clientSwapMs = (performance.now() - timestamps[swapKey]).toFixed(2) + 'ms';
            delete timestamps[swapKey];
        } else {
            data.isOOB = true;
        }
        var reqInfo = requestStarts[requestPath];
        if (reqInfo) {
            data.requestMs = (performance.now() - reqInfo.start).toFixed(2) + 'ms';
        }
        log('info', data.isOOB ? 'htmx:afterSwap (OOB)' : 'htmx:afterSwap', data);
    });

    document.body.addEventListener('htmx:afterSettle', function(e) {
        if (!isActive) return;
        counters.htmxAfterSettle++;
        var detail = e.detail || {};
        var data = {
            target: detail.target ? detail.target.id || detail.target.tagName : null,
            swapStyle: detail.swapStyle,
            pathInfo: detail.pathInfo ? detail.pathInfo.requestPath : null
        };
        log('info', 'htmx:afterSettle', data);
    });

    document.body.addEventListener('htmx:historyRestore', function(e) {
        if (!isActive) return;
        counters.htmxHistoryRestore++;
        var data = {
            url: window.location.href,
            historyLength: history.length
        };
        log('info', 'htmx:historyRestore', data);
    });

    window.addEventListener('popstate', function(e) {
        if (!isActive) return;
        counters.popstate++;
        var activeEl = document.activeElement;
        var data = {
            state: e.state,
            historyLength: history.length,
            url: window.location.href,
            uiState: getUIState(),
            activeElement: activeEl ? (activeEl.tagName + (activeEl.id ? '#' + activeEl.id : '') + (activeEl.className ? '.' + activeEl.className.substring(0, 40) : '')) : 'none',
            overlayInfo: (typeof window.contabaseInspectOverlays === 'function') ? {
                topOverlay: (function() {
                    try {
                        var r = window.contabaseInspectOverlays();
                        return { top: r.topOverlay, count: r.open.length, sentinelPushed: r.sentinelPushed, ownPopstateExpected: r.ownPopstateExpected, lastAction: r.lastAction, lastPopstateDecision: r.lastPopstateDecision, bottomSheetChildren: r.bottomSheetChildren, currentStateIsOverlay: r.currentStateIsOverlay };
                    } catch (ex) { return { error: ex.message }; }
                })()
            } : 'unavailable'
        };
        log('info', 'popstate', data);
    });

    window.contabasePerfReport = function() {
        if (!isActive) {
            console.log('[ContaBase:Debug] Debug nao esta ativo. Ative com: localStorage.setItem("contabase_debug_nav", "1")');
            return null;
        }
        var report = {
            timestamp: new Date().toISOString(),
            counters: Object.assign({}, counters),
            historyLength: history.length,
            url: window.location.href,
            uiState: getUIState(),
            logCount: logs.length
        };
        console.log('[ContaBase:Debug] Performance Report:', report);
        return report;
    };

    window.contabaseInspectHistory = function() {
        if (!isActive) {
            console.log('[ContaBase:Debug] Debug nao esta ativo. Ative com: localStorage.setItem("contabase_debug_nav", "1")');
            return null;
        }
        var result = {
            historyLength: history.length,
            url: window.location.href,
            htmxHistoryCache: null,
            htmxConfig: {
                historyEnabled: (typeof htmx !== 'undefined' && htmx.config) ? htmx.config.historyEnabled : 'unknown',
                historyCacheSize: (typeof htmx !== 'undefined' && htmx.config) ? htmx.config.historyCacheSize : 'unknown',
                refreshOnHistoryMiss: (typeof htmx !== 'undefined' && htmx.config) ? htmx.config.refreshOnHistoryMiss : 'unknown',
                allowEval: (typeof htmx !== 'undefined' && htmx.config) ? htmx.config.allowEval : 'unknown',
                expectedCacheSuppressed: 'historyCacheSize should be 0 for security'
            }
        };
        try {
            var cache = localStorage.getItem('htmx-history-cache');
            if (cache) {
                var parsed = JSON.parse(cache);
                result.htmxHistoryCache = {
                    entries: Array.isArray(parsed) ? parsed.length : 'not-array',
                    size: cache.length + ' bytes'
                };
            } else {
                result.htmxHistoryCache = { entries: 0, size: '0 bytes' };
            }
        } catch (e) {
            result.htmxHistoryCache = { error: e.message };
        }
        console.log('[ContaBase:Debug] History Inspection:', result);
        return result;
    };

    window.contabaseInspectModals = function() {
        if (!isActive) {
            console.log('[ContaBase:Debug] Debug nao esta ativo. Ative com: localStorage.setItem("contabase_debug_nav", "1")');
            return null;
        }
        var state = getUIState();
        console.log('[ContaBase:Debug] Modal/UI State:', state);
        return state;
    };

    window.contabaseDebugLogs = function(count) {
        if (!isActive) {
            console.log('[ContaBase:Debug] Debug nao esta ativo. Ative com: localStorage.setItem("contabase_debug_nav", "1")');
            return null;
        }
        var n = count || 50;
        var slice = logs.slice(-n);
        console.log('[ContaBase:Debug] Last ' + slice.length + ' logs:', slice);
        return slice;
    };

    log('info', 'Debug instrumentation ativada (D8)', {
        flag: FLAG_KEY,
        url: window.location.href,
        historyLength: history.length,
        version: 'D8'
    });

})();