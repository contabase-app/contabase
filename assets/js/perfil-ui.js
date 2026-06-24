// Profile Page UI Handlers — extracted from configuracoes_perfil.html (F2.6-B, Fase C4)
(function () {
    'use strict';

    function initPasswordFormToggle() {
        var toggleBtn = document.getElementById('togglePasswordForm');
        var cancelBtn = document.getElementById('cancelPasswordForm');
        var formArea = document.getElementById('passwordFormArea');

        if (toggleBtn && formArea) {
            toggleBtn.removeEventListener('click', togglePasswordClick);
            toggleBtn.addEventListener('click', togglePasswordClick);
        }
        if (cancelBtn && formArea) {
            cancelBtn.removeEventListener('click', cancelPasswordClick);
            cancelBtn.addEventListener('click', cancelPasswordClick);
        }
    }

    function togglePasswordClick() {
        var formArea = document.getElementById('passwordFormArea');
        if (formArea) {
            formArea.classList.toggle('hidden');
            if (!formArea.classList.contains('hidden')) {
                var firstInput = formArea.querySelector('input[name="current_password"]');
                if (firstInput) firstInput.focus();
            }
        }
    }

    function cancelPasswordClick() {
        var formArea = document.getElementById('passwordFormArea');
        if (formArea) formArea.classList.add('hidden');
        var form = document.getElementById('passwordChangeForm');
        if (form) form.reset();
    }

    // CSP-safe: replaces inline onclick on "Sair dos outros dispositivos" button
    window.revokeOtherSessions = function() {
        window.openGlobalConfirm(
            'Sair dos outros dispositivos? Este aparelho permanecera conectado, mas todos os outros serao desconectados.',
            function() {
                htmx.ajax('POST', '/configuracoes/perfil/sessoes/revogar-outras', {
                    target: '#settings-dynamic-payload',
                    swap: 'outerHTML'
                });
            },
            true
        );
    };

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', initPasswordFormToggle);
    } else {
        initPasswordFormToggle();
    }

    document.body.addEventListener('htmx:afterSwap', function (e) {
        var target = e.detail && e.detail.target;
        if (target && (target.id === 'passwordFormArea' || target.id === 'main-content' || target.id === 'settings-dynamic-payload')) {
            initPasswordFormToggle();
        }
    });
})();
