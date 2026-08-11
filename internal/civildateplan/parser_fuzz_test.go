package civildateplan

import (
	"testing"
)

func FuzzParsePlanV1(f *testing.F) {
	f.Add(planJSON(nrFinding()))
	f.Add(planJSON(rcrFinding()))
	f.Add(planJSON(ruleFinding()))
	f.Add("{}")
	f.Add(`{"schema_version":1,"plan_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`)
	f.Add(`{"schema_version":1,"plan_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","scope":"DATABASE","duplicate":1,"duplicate":1}`)
	f.Add(`{"schema_version":1,"plan_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","scope":"DATABASE","unknown_field":42}`)
	f.Add(`{"schema_version":1,"plan_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","scope":"DATABASE","findings":[{"invalid\x00":"x"}]}`)
	f.Add(func() string {
		s := `{"a":`
		for i := 0; i < 40; i++ {
			s += `{"a":`
		}
		s += `1`
		for i := 0; i < 40; i++ {
			s += `}`
		}
		s += `}`
		return s
	}())
	f.Add(`{"schema_version":"wrong_type"}`)

	f.Fuzz(func(t *testing.T, data string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic: %v", r)
			}
		}()
		plan, err := parsePlanV1([]byte(data))
		_ = plan
		_ = err
	})
}
