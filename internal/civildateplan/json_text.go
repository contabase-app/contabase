package civildateplan

import (
	"fmt"
)

func validateLexical(data []byte) error {
	max := len(data)
	inString := false
	i := 0

	for i < max {
		b := data[i]

		if !inString {
			if b < 0x20 && b != '\t' && b != '\n' && b != '\r' && b != ' ' {
				return fmt.Errorf("invalid control character 0x%02x outside string at offset %d", b, i)
			}
			if b == '"' {
				inString = true
			}
			i++
			continue
		}

		if b == '"' {
			inString = false
			i++
			continue
		}

		if b < 0x20 {
			return fmt.Errorf("unescaped control character 0x%02x in string at offset %d", b, i)
		}

		if b == '\\' {
			i++
			if i >= max {
				return fmt.Errorf("incomplete escape at end of input")
			}
			esc := data[i]
			switch esc {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				i++
			case 'u':
				i++
				r1, consumed, err := readHexEscape(data, i, max)
				if err != nil {
					return err
				}
				i = consumed
				if utf16IsHighSurrogate(r1) {
					if i+6 > max || data[i] != '\\' || data[i+1] != 'u' {
						return fmt.Errorf("isolated high surrogate at offset %d", i-6)
					}
					i += 2
					r2, consumed2, err := readHexEscape(data, i, max)
					if err != nil {
						return err
					}
					if !utf16IsLowSurrogate(r2) {
						return fmt.Errorf("expected low surrogate after high surrogate at offset %d", i-2)
					}
					i = consumed2
				} else if utf16IsLowSurrogate(r1) {
					return fmt.Errorf("isolated low surrogate at offset %d", i-6)
				}
			default:
				return fmt.Errorf("invalid escape \\%c at offset %d", esc, i-1)
			}
		} else {
			i++
		}
	}

	if inString {
		return fmt.Errorf("unterminated string at end of input")
	}

	return nil
}

func readHexEscape(data []byte, i, max int) (rune, int, error) {
	if i+4 > max {
		return 0, 0, fmt.Errorf("incomplete \\u escape at offset %d", i-2)
	}
	var v rune
	for j := 0; j < 4; j++ {
		c := data[i+j]
		var d rune
		switch {
		case c >= '0' && c <= '9':
			d = rune(c - '0')
		case c >= 'a' && c <= 'f':
			d = rune(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = rune(c-'A') + 10
		default:
			return 0, 0, fmt.Errorf("invalid hex char %c in \\u escape at offset %d", c, i-2)
		}
		v = v*16 + d
	}
	return v, i + 4, nil
}

func utf16IsHighSurrogate(r rune) bool { return r >= 0xD800 && r <= 0xDBFF }
func utf16IsLowSurrogate(r rune) bool  { return r >= 0xDC00 && r <= 0xDFFF }
