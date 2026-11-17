// Feedback persistente da importação de backup (B2).
// Estados conhecidos do cliente: arquivo selecionado, enviando, falha
// confirmada (resposta do servidor) e resultado indeterminado (rede/timeout).
// O sucesso é sinalizado só pelo servidor (HX-Trigger e HX-Redirect).
// O cliente não observa as etapas internas do servidor (validação, restore),
// portanto não as informa nem inventa progresso.
(function() {
  // Espelha adminBackupImportMaxBodyBytes (cmd/server/main.go): 50 MiB do corpo
  // multipart. A margem cobre boundary e cabeçalhos da parte do arquivo.
  var MAX_BODY_BYTES = 50 * 1024 * 1024;
  var MULTIPART_MARGIN_BYTES = 1024;

  var MSG_INDETERMINADO = 'Não foi possível confirmar o resultado da importação. Verifique o estado do sistema antes de tentar novamente.';

  var busy = false;

  function importForm(elt) {
    while (elt && elt !== document.body) {
      if (elt.matches && elt.matches('form[data-backup-import-form]')) return elt;
      elt = elt.parentElement;
    }
    return null;
  }

  function stateEl(form) {
    return form ? form.querySelector('[data-backup-import-state]') : null;
  }

  function statusEl(form) {
    return form ? form.querySelector('[data-backup-import-status]') : null;
  }

  function setState(form, text) {
    var el = stateEl(form);
    if (!el) return;
    if (!text) {
      el.classList.add('hidden');
      el.textContent = '';
      return;
    }
    el.textContent = text;
    el.classList.remove('hidden');
  }

  function selectedFileLabel(form) {
    var input = form ? form.querySelector('input[type="file"][name="backup_file"]') : null;
    if (!input || !input.files || input.files.length === 0) return '';
    var file = input.files[0];
    var sizeMiB = (file.size / (1024 * 1024)).toFixed(1);
    return 'Arquivo selecionado: ' + file.name + ' (' + sizeMiB + ' MiB)';
  }

  // kind: 'error' | 'warning' | 'success' | 'info'
  function setStatus(form, kind, message) {
    var box = statusEl(form);
    if (!box) return;
    var text = box.querySelector('[data-backup-import-status-text]');
    if (!text) return;

    box.classList.remove('hidden', 'cb-flash-danger', 'cb-flash-success', 'ui-panel', 'p-4', 'text-sm');
    var icon = box.querySelector('[data-backup-import-status-icon]');
    if (kind === 'success') {
      box.classList.add('admin-flash', 'cb-flash-success');
      box.setAttribute('role', 'status');
      if (icon) icon.setAttribute('data-lucide', 'circle-check');
    } else if (kind === 'info') {
      box.classList.add('ui-panel', 'p-4', 'text-sm');
      box.setAttribute('role', 'status');
      if (icon) icon.setAttribute('data-lucide', 'circle-help');
    } else {
      // 'error' e 'warning' compartilham o tom de alerta: ambos exigem ação
      // do usuário e não podem passar despercebidos.
      box.classList.add('admin-flash', 'cb-flash-danger');
      box.setAttribute('role', 'alert');
      if (icon) icon.setAttribute('data-lucide', 'circle-alert');
    }

    // O role precisa existir antes da mensagem para leitores de tela.
    text.textContent = message;

    if (window.refreshIcons) window.refreshIcons();
    try {
      box.focus();
    } catch (e) {
      // Foco é conforto, não requisito: ignorar falhas de foco.
    }
  }

  function hideStatus(form) {
    var box = statusEl(form);
    if (!box) return;
    // .admin-flash é CSS sem layer e vence .hidden (utilities layer) no
    // display:flex. Remover as classes visuais devolve o controle ao .hidden.
    box.classList.remove('admin-flash', 'cb-flash-danger', 'cb-flash-success', 'ui-panel', 'p-4', 'text-sm');
    box.classList.add('hidden');
  }

  // getResponseHeader entrega acentuação como latin-1 (mojibake de UTF-8):
  // recompor os bytes e decodificar como UTF-8 antes de exibir.
  function headerAsUTF8(value) {
    try {
      var bytes = new Uint8Array(value.length);
      for (var i = 0; i < value.length; i++) bytes[i] = value.charCodeAt(i) & 0xff;
      return new TextDecoder('utf-8', { fatal: false }).decode(bytes);
    } catch (e) {
      return value;
    }
  }

  function messageFromTrigger(xhr) {
    var raw = xhr.getResponseHeader('HX-Trigger') || '';
    if (!raw) return '';
    try {
      var parsed = JSON.parse(headerAsUTF8(raw));
      if (parsed && parsed.mostrarAlerta) {
        var alert = parsed.mostrarAlerta;
        if (typeof alert === 'string') return alert;
        if (alert && typeof alert.value === 'string') return alert.value;
      }
    } catch (e) {
      // Header não-JSON: cair no fallback por status.
    }
    return '';
  }

  // O htmx processa HX-Trigger antes de htmx:responseError e dispara
  // mostrarAlerta no próprio formulário. O listener global de htmx-lifecycle
  // transformaria a mesma mensagem em toast (com mojibake, pois o header é
  // latin-1). Interceptar na captura e interromper a propagação somente quando
  // a origem for o formulário de importação mantém o painel local como único
  // canal desta operação, sem afetar alertas de outras páginas ou formulários.
  document.addEventListener('mostrarAlerta', function(event) {
    var target = event.target;
    if (!target || !target.closest) return;
    if (!target.closest('form[data-backup-import-form]')) return;
    event.stopPropagation();
  }, true);

  function fallbackMessage(xhr) {
    var retryAfter = parseInt(xhr.getResponseHeader('Retry-After') || '0', 10);
    switch (xhr.status) {
      case 400:
        return 'O backup enviado foi rejeitado pela validação. Use um arquivo .db exportado pelo ContaBase.';
      case 403:
        return 'Sua sessão expirou ou não tem permissão para importar backups. Faça login novamente e repita a operação.';
      case 409:
        return 'O sistema está concluindo outra operação. Aguarde alguns instantes e tente novamente.';
      case 413:
        return 'O backup excede o limite de 50 MiB para importação.';
      case 429:
        if (retryAfter > 0) {
          var minutes = Math.max(1, Math.ceil(retryAfter / 60));
          return 'Muitas tentativas de importação em pouco tempo. Aguarde ' + minutes + (minutes === 1 ? ' minuto' : ' minutos') + ' e tente novamente.';
        }
        return 'Muitas tentativas de importação em pouco tempo. Aguarde alguns minutos e tente novamente.';
      default:
        if (xhr.status >= 500) {
          return 'Erro interno do servidor ao importar o backup. A mensagem detalhada está nos logs da instalação.';
        }
        return 'A importação não foi concluída (HTTP ' + xhr.status + ').';
    }
  }

  function statusKind(xhr) {
    if (xhr.status === 429 || xhr.status === 409) return 'error';
    if (xhr.status >= 500) return 'warning';
    return 'error';
  }

  function validateSelectedFile(form) {
    var input = form.querySelector('input[type="file"][name="backup_file"]');
    if (!input || !input.files || input.files.length === 0) {
      return 'Selecione um arquivo de backup antes de importar.';
    }
    var file = input.files[0];
    var name = String(file.name || '').toLowerCase();
    if (!name.endsWith('.db')) {
      return 'Arquivo inválido: a importação aceita somente backups SQLite com extensão .db.';
    }
    if (file.size > MAX_BODY_BYTES - MULTIPART_MARGIN_BYTES) {
      var sizeMiB = (file.size / (1024 * 1024)).toFixed(1);
      return 'O arquivo selecionado (' + sizeMiB + ' MiB) excede o limite de 50 MiB para importação. Gere um backup menor ou amplie o limite na configuração do servidor.';
    }
    return '';
  }

  // Seleção de arquivo: valida cedo e mantém o chip de estado fiel ao input.
  document.body.addEventListener('change', function(event) {
    var target = event.target;
    if (!target || !target.matches || !target.matches('input[type="file"][name="backup_file"]')) return;
    var form = importForm(target);
    if (!form) return;
    var problem = validateSelectedFile(form);
    if (problem) {
      setStatus(form, 'error', problem);
      setState(form, '');
    } else {
      hideStatus(form);
      setState(form, selectedFileLabel(form));
    }
  });

  // Anti-duplo-envio na fonte: captura o submit antes de o htmx vê-lo. Sem
  // isso, o hx-sync="this:drop" descartaria a segunda submissão ANTES do
  // htmx:configRequest — clique ignorado em silêncio, exatamente o que a
  // fase elimina.
  document.body.addEventListener('submit', function(event) {
    var form = importForm(event.target);
    if (!form || !busy) return;
    event.preventDefault();
    event.stopImmediatePropagation();
    setStatus(form, 'error', 'Uma importação já está em andamento. Aguarde a conclusão antes de iniciar outra.');
  }, true);

  // Pré-validação e anti-duplo-envio antes de o htmx montar a requisição.
  document.body.addEventListener('htmx:configRequest', function(event) {
    var form = importForm(event.target);
    if (!form) return;
    if (busy) {
      event.preventDefault();
      setStatus(form, 'error', 'Uma importação já está em andamento. Aguarde a conclusão antes de iniciar outra.');
      return;
    }
    var problem = validateSelectedFile(form);
    if (problem) {
      event.preventDefault();
      setStatus(form, 'error', problem);
      setState(form, '');
      return;
    }
    busy = true;
    hideStatus(form);
    setState(form, 'Enviando backup e aguardando o servidor…');
  });

  document.body.addEventListener('htmx:responseError', function(event) {
    var detail = event.detail || {};
    var form = importForm(detail.elt);
    if (!form) return;
    busy = false;
    var xhr = detail.xhr;
    if (!xhr) return;
    var message = messageFromTrigger(xhr) || fallbackMessage(xhr);
    setStatus(form, statusKind(xhr), message);
    setState(form, 'Importação não concluída.');
  });

  // Falha de rede, conexão recusada ou perdida: o resultado do restore no
  // servidor é desconhecido — jamais afirmar cancelamento ou reversão.
  document.body.addEventListener('htmx:sendError', function(event) {
    var form = importForm(event.target);
    if (!form) return;
    busy = false;
    setStatus(form, 'warning', MSG_INDETERMINADO + ' A conexão com o servidor foi interrompida durante o envio.');
    setState(form, 'Resultado não confirmado.');
  });

  // Watchdog de timeout próprio. O htmx 2.x não possui hx-timeout por
  // elemento (só htmx.config.timeout global), então a rota de importação
  // aborta por conta própria aos 150s — acima do WriteTimeout do servidor
  // (120s). A operação pode ter sido concluída no servidor mesmo sem
  // resposta: estado indeterminado, nunca "cancelada".
  var IMPORT_TIMEOUT_MS = 150000;
  var inflight = null;

  function importTimeout(form, xhr) {
    inflight = null;
    busy = false;
    // abort() despacha htmx:sendAbort de forma síncrona: abortar ANTES de
    // escrever o status, senão a mensagem de cancelamento sobrescreve a de
    // timeout.
    try {
      if (xhr) xhr.abort();
    } catch (e) {
      // Abort já concluído: ignorar.
    }
    setStatus(form, 'warning', MSG_INDETERMINADO + ' O servidor demorou demais para responder.');
    setState(form, 'Resultado não confirmado.');
  }

  document.body.addEventListener('htmx:beforeRequest', function(event) {
    var form = importForm(event.target);
    if (!form) return;
    var detail = event.detail || {};
    if (inflight && inflight.timer) clearTimeout(inflight.timer);
    inflight = {
      xhr: detail.xhr || null,
      timer: setTimeout(function() {
        importTimeout(form, inflight ? inflight.xhr : null);
      }, IMPORT_TIMEOUT_MS),
    };
  });

  function clearInflight() {
    if (inflight && inflight.timer) clearTimeout(inflight.timer);
    inflight = null;
  }

  // Back/forward com bfcache ou cache de histórico do htmx restaura o estado
  // JavaScript da página. Se havia envio em andamento, o resultado é
  // desconhecido: liberar o formulário sem afirmar cancelamento ou reversão.
  function resetRestoredImport() {
    var wasBusy = busy;
    busy = false;
    clearInflight();
    if (!wasBusy) return;
    var form = document.querySelector('form[data-backup-import-form]');
    if (!form) return;
    setStatus(form, 'warning', MSG_INDETERMINADO + ' A página foi restaurada após navegação; o envio anterior pode ter sido interrompido.');
    setState(form, 'Resultado não confirmado.');
  }

  window.addEventListener('pageshow', function(event) {
    if (event && event.persisted) resetRestoredImport();
  });

  document.body.addEventListener('htmx:historyRestore', function() {
    resetRestoredImport();
  });

  // Timeout do cliente (hx-timeout): a operação pode ter sido concluída no
  // servidor mesmo sem resposta; exigir confirmação antes de nova tentativa.
  document.body.addEventListener('htmx:timeout', function(event) {
    var form = importForm(event.target);
    if (!form) return;
    busy = false;
    setStatus(form, 'warning', MSG_INDETERMINADO + ' O servidor demorou demais para responder.');
    setState(form, 'Resultado não confirmado.');
  });

  document.body.addEventListener('htmx:sendAbort', function(event) {
    var form = importForm(event.target);
    if (!form) return;
    busy = false;
    setStatus(form, 'warning', 'O envio foi cancelado antes de completar. ' + MSG_INDETERMINADO);
    setState(form, 'Envio cancelado.');
  });

  // Rede de segurança: qualquer término de requisição devolve o formulário
  // a um estado coerente (spinner/botão refletem o estado real).
  document.body.addEventListener('htmx:afterRequest', function(event) {
    var form = importForm(event.target);
    if (!form) return;
    busy = false;
    clearInflight();
  });

  // Somente o término da própria importação pode liberar novo envio. Um
  // afterSettle de outra navegação/aba não pode destravar o formulário com
  // uma importação ainda em andamento.
  document.body.addEventListener('htmx:afterSettle', function(event) {
    var detail = event.detail || {};
    var pathInfo = detail.pathInfo || {};
    var requestPath = String(pathInfo.requestPath || '').split('?')[0];
    if (requestPath !== '/admin/backups/importar') return;
    busy = false;
  });

  document.body.addEventListener('click', function(event) {
    var dismiss = event.target.closest ? event.target.closest('[data-backup-import-dismiss]') : null;
    if (!dismiss) return;
    var form = importForm(dismiss);
    if (!form) return;
    hideStatus(form);
  });
})();
