package handlers

// Tons semânticos compartilhados pela interface do ContaBase.
//
// A regra de cor vive aqui, em um único lugar, e os templates apenas usam
// `tone-<nome>`, que o CSS mapeia para tokens (--v2-tone-*). Isso evita
// espalhar limiares numéricos e cores soltas por template, handler e página.
//
// Direção definida pelo produto:
//   - verde  -> positivo, disponível, receita, sucesso;
//   - vermelho -> atraso, déficit, erro, negativo relevante;
//   - amarelo/âmbar -> atenção, aproximação de limite;
//   - roxo -> identidade ContaBase, progresso neutro, interação, seleção;
//   - azul/ciano -> informação.
const (
	// ToneOK: consumo dentro do previsto (positivo).
	ToneOK = "ok"
	// ToneAttention: começa a chamar atenção.
	ToneAttention = "attention"
	// ToneNear: próximo do limite.
	ToneNear = "near"
	// ToneExceeded: limite atingido ou excedido (também usado como negativo).
	ToneExceeded = "exceeded"
	// ToneProgress: progresso neutro de uma Meta/reserva (não é consumo).
	ToneProgress = "progress"
	// ToneComplete: meta concluída.
	ToneComplete = "complete"
	// ToneInfo: informação, sem juízo de valor.
	ToneInfo = "info"
	// ToneNeutral: estado neutro.
	ToneNeutral = "neutral"
	// ToneDanger: problema real, como atraso.
	ToneDanger = "danger"
	// ToneInitial: começo de uma Meta/reserva, ainda longe do alvo.
	ToneInitial = "initial"
)

// Faixas de consumo de um limite mensal. Centralizadas para que dashboard,
// página de metas e relatórios usem exatamente os mesmos cortes.
const (
	limitAttentionPercent = 70
	limitNearPercent      = 90
	limitExceededPercent  = 100
)

// Faixas de evolução de uma Meta/reserva em direção ao alvo. Assim como os
// cortes de consumo, vivem aqui para que dashboard e /metas compartilhem a
// mesma leitura de estado.
const (
	progressInfoMinPercent  = 25 // 25–69%: evolução intermediária.
	progressAdvancedPercent = 70 // 70–99%: objetivo próximo.
)

// limitToneForPercent devolve a faixa semântica de consumo de um limite.
func limitToneForPercent(percent int) string {
	switch {
	case percent >= limitExceededPercent:
		return ToneExceeded
	case percent >= limitNearPercent:
		return ToneNear
	case percent >= limitAttentionPercent:
		return ToneAttention
	default:
		return ToneOK
	}
}

// limitToneLabel devolve o rótulo acessível da faixa de consumo.
func limitToneLabel(tone string) string {
	switch tone {
	case ToneExceeded:
		return "Limite atingido"
	case ToneNear:
		return "Perto do limite"
	case ToneAttention:
		return "Atenção"
	default:
		return "Dentro do previsto"
	}
}

// progressToneFor devolve a faixa de uma Meta/reserva, que representa avanço
// (não consumo) e por isso não compartilha os cortes de Limite:
//   - negativa   -> exceeded (estado problemático);
//   - 0–24%      -> initial (começo da reserva, longe do alvo);
//   - 25–69%     -> info (evolução intermediária);
//   - 70–99%     -> ok (objetivo próximo);
//   - >= 100%    -> complete (objetivo atingido).
func progressToneFor(percent int, isNegative bool) string {
	switch {
	case isNegative:
		return ToneExceeded
	case percent >= 100:
		return ToneComplete
	case percent >= progressAdvancedPercent:
		return ToneOK
	case percent >= progressInfoMinPercent:
		return ToneInfo
	default:
		return ToneInitial
	}
}

// progressToneLabel devolve o rótulo acessível do progresso da Meta.
func progressToneLabel(tone string) string {
	switch tone {
	case ToneExceeded:
		return "Reserva negativa"
	case ToneComplete:
		return "Meta atingida"
	case ToneOK:
		return "Perto do alvo"
	case ToneInitial:
		return "Início da reserva"
	default:
		return "Em andamento"
	}
}
