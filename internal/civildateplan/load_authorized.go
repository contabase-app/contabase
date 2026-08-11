package civildateplan

import (
	"encoding/hex"
	"strings"
)

// LoadAuthorizedRepairPlan reads a repair plan from path, prepares it
// structurally and semantically, validates the approved digest, and returns
// an authorized capability. It is the only exported function of this package
// that constructs an *AuthorizedRepairPlan.
func LoadAuthorizedRepairPlan(
	path string,
	approvedDigest string,
) (*AuthorizedRepairPlan, error) {
	data, err := readPlanFileBytes(path)
	if err != nil {
		return nil, err
	}

	state, calculated, err := preparePlanBytes(data)
	if err != nil {
		return nil, err
	}

	approved, err := parseApprovedPlanDigestStrict(approvedDigest)
	if err != nil {
		return nil, err
	}

	if calculated != [32]byte(approved) {
		return nil, newLoadError(loadErrorApprovedDigestMismatch)
	}

	plan := &AuthorizedRepairPlan{
		seal:   authorizedPlanSeal,
		state:  &state,
		digest: calculated,
	}

	if !plan.IsAuthorized() {
		return nil, newLoadError(loadErrorInternalInvariant)
	}

	return plan, nil
}

type approvedPlanDigest [32]byte

func parseApprovedPlanDigestStrict(value string) (approvedPlanDigest, error) {
	const prefix = "sha256:"
	const totalLen = 71

	if len(value) != totalLen {
		return approvedPlanDigest{}, newLoadError(loadErrorApprovedDigestInvalid)
	}
	if !strings.HasPrefix(value, prefix) {
		return approvedPlanDigest{}, newLoadError(loadErrorApprovedDigestInvalid)
	}

	hexPart := value[len(prefix):]
	if len(hexPart) != 64 {
		return approvedPlanDigest{}, newLoadError(loadErrorApprovedDigestInvalid)
	}

	for i := 0; i < len(hexPart); i++ {
		c := hexPart[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return approvedPlanDigest{}, newLoadError(loadErrorApprovedDigestInvalid)
		}
	}

	var result approvedPlanDigest
	_, err := hex.Decode(result[:], []byte(hexPart))
	if err != nil {
		return approvedPlanDigest{}, newLoadError(loadErrorApprovedDigestInvalid)
	}

	return result, nil
}
