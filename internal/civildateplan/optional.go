package civildateplan

type optionalState uint8

const (
	optionalInvalid optionalState = iota
	optionalNull
	optionalValue
)

type OptionalString struct {
	state optionalState
	val   string
}

func (o OptionalString) IsNull() bool { return o.state == optionalNull }

func (o OptionalString) Value() (string, bool) {
	if o.state == optionalValue {
		return o.val, true
	}
	return "", false
}

type OptionalInt64 struct {
	state optionalState
	val   int64
}

func (o OptionalInt64) IsNull() bool { return o.state == optionalNull }

func (o OptionalInt64) Value() (int64, bool) {
	if o.state == optionalValue {
		return o.val, true
	}
	return 0, false
}

type OptionalBool struct {
	state optionalState
	val   bool
}

func (o OptionalBool) IsNull() bool { return o.state == optionalNull }

func (o OptionalBool) Value() (bool, bool) {
	if o.state == optionalValue {
		return o.val, true
	}
	return false, false
}

func newOptionalString(v string) OptionalString {
	return OptionalString{state: optionalValue, val: v}
}

func newOptionalStringNull() OptionalString {
	return OptionalString{state: optionalNull}
}

func newOptionalInt64(v int64) OptionalInt64 {
	return OptionalInt64{state: optionalValue, val: v}
}

func newOptionalInt64Null() OptionalInt64 {
	return OptionalInt64{state: optionalNull}
}

func newOptionalBool(v bool) OptionalBool {
	return OptionalBool{state: optionalValue, val: v}
}

func (o OptionalString) valid() bool { return o.state == optionalNull || o.state == optionalValue }
func (o OptionalInt64) valid() bool  { return o.state == optionalNull || o.state == optionalValue }
func (o OptionalBool) valid() bool   { return o.state == optionalNull || o.state == optionalValue }
