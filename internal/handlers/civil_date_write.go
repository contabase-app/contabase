package handlers

import (
	"database/sql"
	"fmt"

	"github.com/contabase-app/contabase/internal/civildate"
)

// CivilWriteDate is the single source for a mandatory financial civil date.
// Its legacy INTEGER is always derived from the validated civil value.
type CivilWriteDate struct {
	date civildate.CivilDate
}

func NewCivilWriteDate(input string) (CivilWriteDate, error) {
	date, err := civildate.ParseCivilDateInput(input)
	if err != nil {
		return CivilWriteDate{}, err
	}
	return CivilWriteDate{date: date}, nil
}

func MustCivilWriteDate(input string) CivilWriteDate {
	date, err := NewCivilWriteDate(input)
	if err != nil {
		panic(err)
	}
	return date
}

func (date CivilWriteDate) Civil() string {
	return civildate.FormatCivilDate(date.date)
}

func (date CivilWriteDate) LegacyUnix() int64 {
	return civildate.CivilDateToLegacyUnixNoonUTC(date.date)
}

func (date CivilWriteDate) AddMonths(months int) (CivilWriteDate, error) {
	shifted, err := civildate.CivilDateAddMonths(date.date, months)
	if err != nil {
		return CivilWriteDate{}, fmt.Errorf("add civil months: %w", err)
	}
	return CivilWriteDate{date: shifted}, nil
}

func (date CivilWriteDate) AddDays(days int) (CivilWriteDate, error) {
	shifted, err := civildate.CivilDateAddDays(date.date, days)
	if err != nil {
		return CivilWriteDate{}, fmt.Errorf("add civil days: %w", err)
	}
	return CivilWriteDate{date: shifted}, nil
}

func (date CivilWriteDate) validate() error {
	_, err := civildate.ParseCivilDateInput(date.Civil())
	return err
}

// OptionalCivilWriteDate represents SET or CLEAR for nullable civil dates.
// The zero value is CLEAR. PRESERVE is represented separately by the caller.
type OptionalCivilWriteDate struct {
	date  CivilWriteDate
	valid bool
}

func NewOptionalCivilWriteDate(input string) (OptionalCivilWriteDate, error) {
	if input == "" {
		return OptionalCivilWriteDate{}, nil
	}
	date, err := NewCivilWriteDate(input)
	if err != nil {
		return OptionalCivilWriteDate{}, err
	}
	return OptionalCivilWriteDate{date: date, valid: true}, nil
}

func MustOptionalCivilWriteDate(input string) OptionalCivilWriteDate {
	date, err := NewOptionalCivilWriteDate(input)
	if err != nil {
		panic(err)
	}
	return date
}

func (date OptionalCivilWriteDate) Valid() bool {
	return date.valid
}

func (date OptionalCivilWriteDate) CivilNull() sql.NullString {
	if !date.valid {
		return sql.NullString{}
	}
	return sql.NullString{String: date.date.Civil(), Valid: true}
}

func (date OptionalCivilWriteDate) LegacyNull() sql.NullInt64 {
	if !date.valid {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: date.date.LegacyUnix(), Valid: true}
}

func (date OptionalCivilWriteDate) CivilValue() interface{} {
	if !date.valid {
		return nil
	}
	return date.date.Civil()
}

func (date OptionalCivilWriteDate) LegacyValue() interface{} {
	if !date.valid {
		return nil
	}
	return date.date.LegacyUnix()
}

func (date OptionalCivilWriteDate) AddMonths(months int) (OptionalCivilWriteDate, error) {
	if !date.valid {
		return OptionalCivilWriteDate{}, nil
	}
	shifted, err := date.date.AddMonths(months)
	if err != nil {
		return OptionalCivilWriteDate{}, err
	}
	return OptionalCivilWriteDate{date: shifted, valid: true}, nil
}

func (date OptionalCivilWriteDate) validate() error {
	if !date.valid {
		return nil
	}
	return date.date.validate()
}

func validatedPersistedCivilDate(civil sql.NullString, legacy int64) (string, error) {
	if civil.Valid {
		parsed, err := civildate.ParseCivilDateInput(civil.String)
		if err != nil {
			return "", err
		}
		return civildate.FormatCivilDate(parsed), nil
	}
	return civildate.LegacyUnixToCivilDateUTC(legacy).String(), nil
}

func validatedPersistedOptionalCivilDate(civil sql.NullString, legacy sql.NullInt64) (sql.NullString, error) {
	if civil.Valid {
		parsed, err := civildate.ParseCivilDateInput(civil.String)
		if err != nil {
			return sql.NullString{}, err
		}
		return sql.NullString{String: civildate.FormatCivilDate(parsed), Valid: true}, nil
	}
	if !legacy.Valid {
		return sql.NullString{}, nil
	}
	return sql.NullString{String: civildate.LegacyUnixToCivilDateUTC(legacy.Int64).String(), Valid: true}, nil
}
