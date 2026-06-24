package handlers

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var currencyNoiseRe = regexp.MustCompile(`[^\d,.\-]`)

// parseCurrency converte strings monetárias comuns em mobile (BR/US) para centavos int64.
func parseCurrency(raw string) (int64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, fmt.Errorf("valor vazio")
	}

	s = currencyNoiseRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, " ", "")

	negative := false
	if strings.HasPrefix(s, "-") {
		negative = true
		s = strings.TrimPrefix(s, "-")
	}

	normalized, err := normalizeDecimalString(s)
	if err != nil {
		return 0, err
	}

	parts := strings.SplitN(normalized, ".", 2)
	reaisPart := strings.ReplaceAll(parts[0], ",", "")
	if reaisPart == "" {
		reaisPart = "0"
	}

	reais, err := strconv.ParseInt(reaisPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parte inteira invalida: %w", err)
	}

	var centavos int64
	if len(parts) == 2 && parts[1] != "" {
		frac := parts[1]
		switch len(frac) {
		case 0:
			centavos = 0
		case 1:
			c, err := strconv.ParseInt(frac+"0", 10, 64)
			if err != nil {
				return 0, fmt.Errorf("centavos invalidos: %w", err)
			}
			centavos = c
		default:
			if len(frac) > 2 {
				frac = frac[:2]
			}
			c, err := strconv.ParseInt(frac, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("centavos invalidos: %w", err)
			}
			centavos = c
		}
	}

	total := reais*100 + centavos
	if negative {
		total = -total
	}
	if total <= 0 {
		return 0, fmt.Errorf("valor deve ser positivo")
	}
	return total, nil
}

func normalizeDecimalString(s string) (string, error) {
	if s == "" {
		return "", fmt.Errorf("valor vazio")
	}

	lastComma := strings.LastIndex(s, ",")
	lastDot := strings.LastIndex(s, ".")

	switch {
	case lastComma >= 0 && lastDot >= 0:
		if lastComma > lastDot {
			// BR: 1.234,56
			s = strings.ReplaceAll(s, ".", "")
			s = strings.ReplaceAll(s, ",", ".")
		} else {
			// US: 1,234.56
			s = strings.ReplaceAll(s, ",", "")
		}
	case lastComma >= 0:
		parts := strings.Split(s, ",")
		if len(parts) == 2 && len(parts[1]) <= 2 {
			s = strings.ReplaceAll(parts[0], ".", "") + "." + parts[1]
		} else {
			s = strings.ReplaceAll(s, ",", "")
			s = strings.ReplaceAll(s, ".", "")
		}
	case lastDot >= 0:
		parts := strings.Split(s, ".")
		if len(parts) == 2 && len(parts[1]) <= 2 {
			s = strings.ReplaceAll(parts[0], ",", "") + "." + parts[1]
		} else {
			s = strings.ReplaceAll(s, ".", "")
		}
	default:
		s = strings.ReplaceAll(s, ",", "")
	}

	if s == "" || s == "." {
		return "", fmt.Errorf("valor invalido")
	}
	return s, nil
}
