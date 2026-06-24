// HTMX Lifecycle Handlers
// Extracted from layout.html — Fase 9.9.5e
// Phase C — Lifecycle optimization: debounced icons, deduped initAppShell, scoped roots

try { localStorage.removeItem('htmx-history-cache'); } catch (e) {}

document.body.addEventListener('htmx:configRequest', function(event) {
    var token = csrfToken();
    if (token) {
        event.detail.headers['X-CSRF-Token'] = token;
    }
});

document.body.addEventListener('htmx:configRequest', function(event) {
    var el = event.detail.elt;
    if (!el || !el.closest('[data-preserve-month]')) return;
    var params = new URLSearchParams(window.location.search);
    var mes = params.get('mes');
    var ano = params.get('ano');
    var periodo = params.get('periodo');
    if (!mes && periodo) {
        var parts = periodo.split('-');
        if (parts.length === 2) {
            ano = parts[0];
            mes = String(parseInt(parts[1], 10));
        }
    }
    if (!mes || !ano) return;
    var path = (event.detail.path || '').split('?')[0];
    if (path === '/contas') {
        if (!event.detail.parameters.periodo) {
            event.detail.parameters.periodo = ano + '-' + (mes.length === 1 ? '0' + mes : mes);
        }
    } else {
        if (!event.detail.parameters.mes) event.detail.parameters.mes = mes;
        if (!event.detail.parameters.ano) event.detail.parameters.ano = ano;
    }
});

function runAppShellTask(name, fn, args) {
    if (typeof fn !== 'function') return;
    try {
        fn.apply(window, args || []);
    } catch (error) {
        if (typeof window.contabaseDebugUI === 'function') {
            window.contabaseDebugUI('app shell task failed:', name, error);
        }
        console.error('[ContaBase:UI] app shell task failed:', name, error);
    }
}

function normalizeAppShellRoot(root) {
    if (!root) return document;
    if (root.nodeType === Node.DOCUMENT_NODE) return root;
    if (root.nodeType !== Node.ELEMENT_NODE) return document;
    var tag = root.tagName;
    if (tag === 'SCRIPT' || tag === 'STYLE' || tag === 'LINK' || tag === 'META' || tag === 'TEMPLATE') return document;
    return root;
}

function refreshLucideIcons() {
    if (window.lucide && typeof window.lucide.createIcons === 'function') {
        window.lucide.createIcons();
    }
}

var _refreshIconsRaf = null;

window.refreshIcons = function() {
    if (_refreshIconsRaf) return;
    _refreshIconsRaf = requestAnimationFrame(function() {
        _refreshIconsRaf = null;
        refreshLucideIcons();
    });
};

var lastWorkspaceContextRefreshAt = 0;
function refreshWorkspaceContextThrottled() {
    if (typeof window.refreshWorkspaceTypeUI !== 'function') return;
    var now = Date.now();
    if (now - lastWorkspaceContextRefreshAt < 120) return;
    lastWorkspaceContextRefreshAt = now;
    window.refreshWorkspaceTypeUI();
}

var _initAppShellDocLastTs = 0;

window.initAppShell = function(root) {
    var scope = normalizeAppShellRoot(root);
    var isDoc = (scope === document || scope === document.documentElement);

    if (isDoc && (Date.now() - _initAppShellDocLastTs) < 32) return;
    if (isDoc) _initAppShellDocLastTs = Date.now();

    runAppShellTask('refreshIcons', window.refreshIcons);
    runAppShellTask('initSwipeRows', window.initSwipeRows, [scope]);
    runAppShellTask('initDashboardTabs', window.initDashboardTabs, [scope]);
    runAppShellTask('initPrivacyToggle', window.initPrivacyToggle, [scope]);
    runAppShellTask('initClickTooltips', window.initClickTooltips, [scope]);
    runAppShellTask('centerActiveMonth', window.centerActiveMonth);
    runAppShellTask('refreshWorkspaceTypeUI', refreshWorkspaceContextThrottled);
};

function appShellRootFromEvent(event) {
    if (event && event.detail && event.detail.target) return event.detail.target;
    if (event && event.detail && event.detail.elt) return event.detail.elt;
    if (event && event.target) return event.target;
    return document;
}

var _initShellPending = false;
var _initShellRoot = null;

function initAppShellFromHTMX(event) {
    var root = appShellRootFromEvent(event);
    if (_initShellPending) {
        _initShellRoot = document;
        return;
    }
    _initShellRoot = root;
    _initShellPending = true;
    (typeof queueMicrotask === 'function' ? queueMicrotask : function(fn) { Promise.resolve().then(fn); })(function() {
        _initShellPending = false;
        var scope = _initShellRoot;
        _initShellRoot = null;
        window.initAppShell(scope);
    });
}

function scrollToLocationHash() {
    if (!window.location.hash) return;
    var el = document.querySelector(window.location.hash);
    if (!el) return;
    setTimeout(function() {
        el.scrollIntoView({ behavior: 'smooth', block: 'start' });
    }, 50);
}

if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function() {
        window.initAppShell(document);
    }, { once: true });
} else {
    window.initAppShell(document);
}

document.body.addEventListener('htmx:confirm', function(event) {
    if (!event.detail.question) return;
    
    event.preventDefault();
    
    var isDestructive = false;
    var isStatusChange = false;
    var customTitle = null;

    if (event.detail.elt) {
        if (event.detail.elt.hasAttribute('hx-delete')) isDestructive = true;
        if (event.detail.elt.classList.contains('text-red-600') || event.detail.elt.classList.contains('bg-rose-600')) isDestructive = true;
        var hxPost = event.detail.elt.getAttribute('hx-post') || event.detail.elt.getAttribute('data-hx-post') || '';
        if (hxPost.indexOf('/status-pagamento') !== -1 || event.detail.elt.hasAttribute('data-confirm-status')) {
            isStatusChange = true;
        }
        customTitle = event.detail.elt.getAttribute('data-confirm-title');
    }
    if (!isStatusChange && (event.detail.question.toLowerCase().indexOf('excluir') !== -1 || event.detail.question.toLowerCase().indexOf('remover') !== -1)) {
        isDestructive = true;
    }
    
    window.openGlobalConfirm(event.detail.question, function() {
        event.detail.issueRequest(true);
    }, isDestructive, customTitle);
});

function hasActiveLancamentosFilters() {
    var form = document.getElementById('lancamentosFilters');
    if (!form) return false;
    var q = form.querySelector('input[name="q"]');
    if (q && q.value.trim() !== '') return true;
    var sitTotal = form.querySelectorAll('input[name="situacao"]').length;
    var sitChecked = form.querySelectorAll('input[name="situacao"]:checked').length;
    if (sitChecked > 0 && sitChecked < sitTotal) return true;
    var tipoTotal = form.querySelectorAll('input[name="tipo"]').length;
    var tipoChecked = form.querySelectorAll('input[name="tipo"]:checked').length;
    if (tipoChecked > 0 && tipoChecked < tipoTotal) return true;
    if (form.querySelectorAll('input[name="origem"]:checked').length > 0) return true;
    var cats = form.querySelectorAll('input[name="categoria"]');
    for (var i = 0; i < cats.length; i++) {
        if (cats[i].value.trim() !== '') return true;
    }
    var conta = form.querySelector('input[name="conta"]');
    if (conta && conta.value.trim() !== '') return true;
    return false;
}

document.body.addEventListener('htmx:beforeRequest', function(event) {
    if (!window._swipeActionTs || Date.now() - window._swipeActionTs > 1500) return;
    var elt = event.detail && event.detail.elt;
    if (elt && elt.id === 'lancamentosFilters') {
        var active = hasActiveLancamentosFilters();
        if (typeof window.contabaseDebugUI === 'function') {
            window.contabaseDebugUI('swipe refresh', active ? 'allowed (filters active)' : 'blocked (default view)');
        }
        if (active) return;
        event.preventDefault();
        delete window._swipeActionTs;
    }
});

document.body.addEventListener('htmx:afterSwap', function(event) {
    initAppShellFromHTMX(event);
    scrollToLocationHash();

    var target = event.detail && event.detail.target;
    if (target && (target.id === 'main-content' || target.id === 'settings-content' || target.id === 'settings-dynamic-payload')) {
        if (target.id === 'main-content') {
            var indicator = document.getElementById('page-indicator');
            if (indicator) {
                var pageTitleEl = target.querySelector('[data-page-title]');
                indicator.textContent = pageTitleEl ? pageTitleEl.getAttribute('data-page-title') || '' : '';
            }
        }
        var bs = document.getElementById('bottom-sheet-container');
        if (bs && bs.innerHTML.trim() !== '') {
            bs.innerHTML = '';
            if (typeof window._resetOverlaySentinel === 'function') {
                window._resetOverlaySentinel();
            }
        }
    }
});

document.body.addEventListener('htmx:responseError', function(event) {
    var xhr = event && event.detail && event.detail.xhr;
    if (!xhr) return;
    if (xhr.status === 429) {
        var trigger = xhr.getResponseHeader('HX-Trigger') || '';
        if (trigger.indexOf('mostrarAlerta') !== -1) return;
        var retryAfter = parseInt(xhr.getResponseHeader('Retry-After') || '0', 10);
        if (retryAfter > 0) {
            var minutes = Math.max(1, Math.ceil(retryAfter / 60));
            showGlobalFeedback('Muitas tentativas. Aguarde ' + minutes + (minutes === 1 ? ' minuto' : ' minutos') + ' e tente novamente.', 'error');
            return;
        }
        showGlobalFeedback('Muitas tentativas. Aguarde alguns minutos e tente novamente.', 'error');
        return;
    }
    if (xhr.status === 401) {
        showGlobalFeedback('Sessão expirada. Redirecionando para login.', 'error');
        return;
    }
    if (xhr.status === 403) {
        showGlobalFeedback('Você não tem permissão para esta ação.', 'error');
        return;
    }
    if (xhr.status >= 500) {
        showGlobalFeedback('Erro interno. Tente novamente.', 'error');
        return;
    }
    if (xhr.status >= 400) {
        showGlobalFeedback('Não foi possível concluir a ação.', 'error');
    }
});

document.body.addEventListener('mostrarAlerta', function(event) {
    var detail = event && event.detail;
    var message = '';
    if (typeof detail === 'string') {
        message = detail;
    } else if (detail && typeof detail.value === 'string') {
        message = detail.value;
    }
    if (!message) return;
    showGlobalFeedback(message, 'error');
});

document.body.addEventListener('mostrarSucesso', function(event) {
    var detail = event && event.detail;
    var message = '';
    if (typeof detail === 'string') {
        message = detail;
    } else if (detail && typeof detail.value === 'string') {
        message = detail.value;
    }
    if (!message) return;
    showGlobalFeedback(message, 'success');
});

document.addEventListener('click', function(event) {
    if (event.target.closest('[data-tooltip-trigger]')) return;
    if (event.target.closest('#global-tooltip')) return;
    if (event.target.closest('#lancamentosFilterPanel')) return;
    if (event.target.closest('#bottom-sheet-container')) return;
    hideGlobalTooltip();
});

document.body.addEventListener('htmx:afterSettle', function() {
    document.querySelectorAll('.nav-link.is-active').forEach(function(el) {
        el.classList.remove('is-active');
    });
    var path = window.location.pathname;
    document.querySelectorAll('.nav-link').forEach(function(el) {
        var href = el.getAttribute('href');
        if (href === path || (href === '/' && path === '/')) {
            el.classList.add('is-active');
        }
    });
});

window.addEventListener('pageshow', function(event) {
    if (event.persisted) {
        if (typeof window.closeWorkspaceDrawer === 'function') window.closeWorkspaceDrawer();
        if (typeof window.closePickerModal === 'function') window.closePickerModal();
        if (typeof window.resetOverlayStack === 'function') window.resetOverlayStack();
        fetch('/session/check', {
            method: 'GET',
            credentials: 'same-origin',
            redirect: 'manual',
            cache: 'no-store'
        }).then(function(res) {
            if (res.status !== 204) {
                window.location.href = '/login';
            }
        }).catch(function() {
            if (typeof window.isContaBaseDebugUIEnabled === 'function' && window.isContaBaseDebugUIEnabled()) {
                console.log('[ContaBase:UI] bfcache session revalidation failed (network), keeping page');
            }
        });
    }
});

document.body.addEventListener('htmx:historyRestore', function() {
    if (typeof window.overlaySuppressOpen === 'function') window.overlaySuppressOpen(true);
    if (typeof window.resetOverlayStack === 'function') window.resetOverlayStack();
    window.initAppShell(document);
    if (typeof window.overlaySuppressOpen === 'function') window.overlaySuppressOpen(false);
});

document.body.addEventListener('contabase:logout', function() {
    try { localStorage.removeItem('htmx-history-cache'); } catch (e) {}
    try { sessionStorage.clear(); } catch (e) {}
});
