window.setActiveConfigSection = function (tab) {
    var nav = document.querySelector('[data-config-sidebar]');
    if (!nav) return;

    var activeClasses = 'bg-violet-500/10 text-violet-700 dark:text-violet-300'.split(' ');
    var inactiveClasses = 'text-zinc-500 hover:bg-zinc-100 dark:text-zinc-400 dark:hover:bg-zinc-800/50'.split(' ');

    nav.querySelectorAll('[data-config-section]').forEach(function (link) {
        var isActive = link.getAttribute('data-config-section') === tab;
        if (isActive) {
            inactiveClasses.forEach(function (c) { link.classList.remove(c); });
            activeClasses.forEach(function (c) { link.classList.add(c); });
        } else {
            activeClasses.forEach(function (c) { link.classList.remove(c); });
            inactiveClasses.forEach(function (c) { link.classList.add(c); });
        }
    });
};

(function () {
    function syncSidebarFromPath() {
        var path = window.location.pathname;
        var tab = 'perfil';
        if (path.indexOf('/configuracoes/workspace') !== -1) tab = 'workspace';
        else if (path.indexOf('/configuracoes/categorias') !== -1) tab = 'categorias';
        else if (path.indexOf('/configuracoes/contas') !== -1) tab = 'contas';
        else if (path.indexOf('/configuracoes/cartoes') !== -1) tab = 'cartoes';
        else if (path.indexOf('/admin/usuarios') !== -1) tab = 'admin-users';
        else if (path.indexOf('/admin/workspaces') !== -1) tab = 'admin-workspaces';
        else if (path.indexOf('/admin/backups') !== -1) tab = 'admin-backups';
        else if (path.indexOf('/admin/auditoria') !== -1) tab = 'admin-auditoria';
        window.setActiveConfigSection(tab);
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', syncSidebarFromPath);
    } else {
        syncSidebarFromPath();
    }
    document.addEventListener('htmx:afterSettle', syncSidebarFromPath);
})();
