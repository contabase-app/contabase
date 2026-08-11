package civildateplan

func preparePlanBytes(data []byte) (authorizedPlanState, [32]byte, error) {
	if len(data) == 0 {
		return authorizedPlanState{}, [32]byte{}, newLoadError(loadErrorSyntax)
	}

	validated, err := parseAndValidatePlanV1(data)
	if err != nil {
		kind, ok := planStageErrorKind(err)
		if !ok {
			kind = loadErrorInternalInvariant
		}
		return authorizedPlanState{}, [32]byte{}, newLoadError(kind)
	}

	state, err := buildAuthorizedPlanState(validated)
	if err != nil || state == nil {
		return authorizedPlanState{}, [32]byte{}, newLoadError(loadErrorInternalInvariant)
	}

	return finalizePreparedPlanState(*state)
}

func finalizePreparedPlanState(
	state authorizedPlanState,
) (authorizedPlanState, [32]byte, error) {
	if !state.invariantsValid() {
		return authorizedPlanState{}, [32]byte{}, newLoadError(loadErrorInternalInvariant)
	}
	digest, err := computeAuthorizedPlanDigestV1(&state)
	if err != nil {
		return authorizedPlanState{}, [32]byte{}, newLoadError(loadErrorCanonicalization)
	}

	return state, digest, nil
}
