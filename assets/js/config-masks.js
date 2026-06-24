// Config Pages — Input Masks + Profile Photo Upload Helper
(function() {
    window.maskCnpjCpf = function(input) {
        var d = input.value.replace(/\D/g, '').substring(0, 14);
        if (d.length <= 11) {
            d = d.replace(/(\d{3})(\d)/, '$1.$2').replace(/(\d{3})\.(\d{3})(\d)/, '$1.$2.$3').replace(/\.(\d{3})(\d{1,2})$/, '.$1-$2');
        } else {
            d = d.replace(/^(\d{2})(\d)/, '$1.$2').replace(/^(\d{2})\.(\d{3})(\d)/, '$1.$2.$3').replace(/\.(\d{3})(\d)/, '.$1/$2').replace(/(\d{4})(\d{1,2})$/, '$1-$2');
        }
        input.value = d;
    };

    window.maskPhone = function(input) {
        var d = input.value.replace(/\D/g, '').substring(0, 11);
        if (d.length <= 10) {
            d = d.replace(/^(\d{2})(\d)/, '($1) $2').replace(/(\d{4})(\d{1,4})$/, '$1-$2');
        } else {
            d = d.replace(/^(\d{2})(\d)/, '($1) $2').replace(/(\d{5})(\d{1,4})$/, '$1-$2');
        }
        input.value = d;
    };

    window.submitProfilePhoto = function(input) {
        if (!input || !input.files || input.files.length === 0) return;
        var form = input.closest('form');
        if (!form) return;
        if (typeof showGlobalFeedback === 'function') {
            showGlobalFeedback('Enviando foto de perfil...', 'success');
        }
        form.requestSubmit();
    };
})();
