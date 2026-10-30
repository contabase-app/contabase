// Tabs de tipo de Contatos.
//
// Mesmo contrato das abas de Metas: ativação manual (setas movem o foco;
// Enter/Espaço acionam o HTMX nativo do botão), roving tabindex e foco
// restaurado na aba ativa após o swap de #contatos-list. A semântica
// role="tablist"/role="tab"/aria-selected vem do servidor; aqui só o teclado.
(function () {
    if (window.__contatosTabsController) return;
    window.__contatosTabsController = true;

    var pendingTabFocus = false;

    function contatosTabs() {
        return Array.prototype.slice.call(document.querySelectorAll('#contatos-tabs [role="tab"]'));
    }

    function activeTab() {
        return document.querySelector('#contatos-tabs [role="tab"][aria-selected="true"]');
    }

    function restorePendingTabFocus() {
        if (!pendingTabFocus) return;
        pendingTabFocus = false;
        var active = activeTab();
        if (active && typeof active.focus === 'function') active.focus();
    }

    document.addEventListener('click', function (event) {
        var tab = event.target.closest ? event.target.closest('#contatos-tabs [role="tab"]') : null;
        if (tab) pendingTabFocus = true;
    });

    document.addEventListener('keydown', function (event) {
        var tab = event.target.closest ? event.target.closest('#contatos-tabs [role="tab"]') : null;
        if (!tab || event.ctrlKey || event.metaKey || event.altKey) return;
        var tabs = contatosTabs();
        if (tabs.length < 2) return;
        var index = tabs.indexOf(tab);
        var next = null;
        if (event.key === 'ArrowRight') next = tabs[(index + 1) % tabs.length];
        else if (event.key === 'ArrowLeft') next = tabs[(index - 1 + tabs.length) % tabs.length];
        else if (event.key === 'Home') next = tabs[0];
        else if (event.key === 'End') next = tabs[tabs.length - 1];
        if (!next) return;
        event.preventDefault();
        tabs.forEach(function (item) {
            item.setAttribute('tabindex', item === next ? '0' : '-1');
        });
        next.focus();
    });

    function bind() {
        document.body.addEventListener('htmx:afterSettle', restorePendingTabFocus);
    }

    if (document.body) {
        bind();
    } else {
        document.addEventListener('DOMContentLoaded', bind);
    }
}());
