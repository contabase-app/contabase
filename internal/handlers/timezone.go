package handlers

import (
	"os"
	"strings"
	"time"

	_ "time/tzdata"
)

const defaultBusinessTimezone = "America/Sao_Paulo"

func appTimeLocation() *time.Location {
	for _, key := range []string{"CONTABASE_TIMEZONE", "TZ"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			if loc, err := time.LoadLocation(value); err == nil {
				return loc
			}
		}
	}
	loc, err := time.LoadLocation(defaultBusinessTimezone)
	if err != nil {
		return time.Local
	}
	return loc
}

func businessDayStartUnix(now time.Time) int64 {
	return appDayStartUnix(now)
}

func appDayStartUnix(now time.Time) int64 {
	loc := appTimeLocation()
	current := now.In(loc)
	return time.Date(current.Year(), current.Month(), current.Day(), 0, 0, 0, 0, loc).Unix()
}

func appNextDayStartUnix(now time.Time) int64 {
	loc := appTimeLocation()
	current := now.In(loc)
	startOfDay := time.Date(current.Year(), current.Month(), current.Day(), 0, 0, 0, 0, loc)
	return startOfDay.AddDate(0, 0, 1).Unix()
}

func parseCivilDateStartUnix(value string) (int64, error) {
	value = strings.TrimSpace(value)
	loc := appTimeLocation()
	parsed, err := time.ParseInLocation("2006-01-02", value, loc)
	if err != nil {
		return 0, err
	}
	return time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 0, 0, 0, 0, loc).Unix(), nil
}

func formatAppDateLabelFromUnix(unix int64) string {
	if unix <= 0 {
		return "-"
	}
	return time.Unix(unix, 0).In(appTimeLocation()).Format("02/01/2006")
}
