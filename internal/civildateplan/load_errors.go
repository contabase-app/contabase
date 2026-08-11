package civildateplan

type loadErrorKind uint8

const (
	loadErrorSyntax loadErrorKind = iota + 1
	loadErrorSchema
	loadErrorValidation
	loadErrorCanonicalization
	loadErrorInternalInvariant
	loadErrorInvalidPath
	loadErrorUnsupportedPlatform
	loadErrorOpen
	loadErrorNotRegular
	loadErrorEmpty
	loadErrorTooLarge
	loadErrorRead
	loadErrorChangedDuringRead
	loadErrorApprovedDigestInvalid
	loadErrorApprovedDigestMismatch
)

type loadError struct {
	kind loadErrorKind
}

func (e *loadError) Error() string {
	if e == nil {
		return "repair plan preparation failed"
	}
	switch e.kind {
	case loadErrorSyntax:
		return "invalid plan syntax"
	case loadErrorSchema:
		return "incompatible plan schema"
	case loadErrorValidation:
		return "invalid plan data"
	case loadErrorCanonicalization:
		return "plan canonicalization failed"
	case loadErrorInternalInvariant:
		return "internal plan invariant failed"
	case loadErrorInvalidPath:
		return "invalid path"
	case loadErrorUnsupportedPlatform:
		return "unsupported platform"
	case loadErrorOpen:
		return "open"
	case loadErrorNotRegular:
		return "not regular"
	case loadErrorEmpty:
		return "empty"
	case loadErrorTooLarge:
		return "too large"
	case loadErrorRead:
		return "read"
	case loadErrorChangedDuringRead:
		return "changed during read"
	case loadErrorApprovedDigestInvalid:
		return "invalid approved plan digest"
	case loadErrorApprovedDigestMismatch:
		return "approved plan digest mismatch"
	default:
		return "repair plan preparation failed"
	}
}

type planStageError struct {
	kind    loadErrorKind
	message string
}

func (e *planStageError) Error() string {
	if e == nil {
		return "repair plan parsing failed"
	}
	return e.message
}

func newPlanStageError(kind loadErrorKind, err error) error {
	if err == nil {
		return nil
	}
	return &planStageError{kind: kind, message: err.Error()}
}

func planStageErrorKind(err error) (loadErrorKind, bool) {
	classified, ok := err.(*planStageError)
	if !ok || classified == nil {
		return 0, false
	}
	return classified.kind, true
}

func newLoadError(kind loadErrorKind) error {
	return &loadError{kind: kind}
}
