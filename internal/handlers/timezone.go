package handlers

import "time"

const businessTimezone = "America/Sao_Paulo"

func businessDayStartUnix(now time.Time) int64 {
	loc, err := time.LoadLocation(businessTimezone)
	if err != nil {
		loc = time.Local
	}
	current := now.In(loc)
	startOfDay := time.Date(current.Year(), current.Month(), current.Day(), 0, 0, 0, 0, loc)
	return startOfDay.Unix()
}
