package civildateplan

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
	"unicode/utf8"
)

const domainSeparatorV1 = "civil-date-repair-plan-digest/v1\x00"

const (
	tagNULL   uint8 = 0x00
	tagSTRING uint8 = 0x01
	tagINT64  uint8 = 0x02
	tagBOOL   uint8 = 0x03
	tagOBJECT uint8 = 0x04
	tagARRAY  uint8 = 0x05
)

// ---------- top-level field IDs ----------

const (
	fidSchemaVersion             uint16 = 0x0001
	fidPlanID                    uint16 = 0x0002
	fidScope                     uint16 = 0x0003
	fidToolVersion               uint16 = 0x0004
	fidClassifierVersion         uint16 = 0x0005
	fidDatabaseSchemaVersion     uint16 = 0x0006
	fidTimezone                  uint16 = 0x0007
	fidLogicalFingerprintVersion uint16 = 0x0008
	fidSourceLogicalFingerprint  uint16 = 0x0009
	fidAuditSetDigest            uint16 = 0x000A
	fidGeneratedAt               uint16 = 0x000B
	fidFindings                  uint16 = 0x000C
)

// ---------- finding field IDs ----------

const (
	fidFindingVariant             uint16 = 0x0101
	fidFindingSource              uint16 = 0x0102
	fidFindingWorkspaceID         uint16 = 0x0103
	fidFindingSourceRowID         uint16 = 0x0104
	fidFindingAuditedField        uint16 = 0x0105
	fidFindingReasonCode          uint16 = 0x0106
	fidFindingRepairOutcome       uint16 = 0x0107
	fidFindingExpectedLegacyValue uint16 = 0x0108
	fidFindingExpectedCivilState  uint16 = 0x0109
	fidFindingProposedCivilValue  uint16 = 0x010A
	fidFindingTimezone            uint16 = 0x010B
	fidFindingClassifierVersion   uint16 = 0x010C
	fidFindingTransactionState    uint16 = 0x010D
	fidFindingRuleState           uint16 = 0x010E
	fidFindingSeriesState         uint16 = 0x010F
)

// ---------- transaction state field IDs ----------

const (
	fidTxID                       uint16 = 0x0201
	fidTxWorkspaceID              uint16 = 0x0202
	fidTxStatus                   uint16 = 0x0203
	fidTxType                     uint16 = 0x0204
	fidTxDateUnix                 uint16 = 0x0205
	fidTxDueDateUnix              uint16 = 0x0206
	fidTxRecurringRuleID          uint16 = 0x0207
	fidTxSeriesID                 uint16 = 0x0208
	fidTxRecurrenceSequence       uint16 = 0x0209
	fidTxIsRecurringException     uint16 = 0x020A
	fidTxRecurringExceptionReason uint16 = 0x020B
)

// ---------- rule state field IDs ----------

const (
	fidRuleID                     uint16 = 0x0301
	fidRuleWorkspaceID            uint16 = 0x0302
	fidRuleSeriesID               uint16 = 0x0303
	fidRuleParentRuleID           uint16 = 0x0304
	fidRuleEffectiveStartSequence uint16 = 0x0305
	fidRuleEffectiveEndSequence   uint16 = 0x0306
	fidRuleActive                 uint16 = 0x0307
)

// ---------- series state field IDs ----------

const (
	fidSeriesID          uint16 = 0x0401
	fidSeriesWorkspaceID uint16 = 0x0402
	fidSeriesActive      uint16 = 0x0403
)

// ---------- encoder ----------

func encodeAuthorizedPlanStateV1(state *authorizedPlanState) ([]byte, error) {
	if state == nil {
		return nil, fmt.Errorf("cannot encode nil state")
	}
	var buf bytes.Buffer

	fields := []tlvField{encodeInt64Field(fidSchemaVersion, state.schemaVersion)}
	for _, item := range []struct {
		id    uint16
		value string
	}{
		{fidPlanID, state.planID},
		{fidScope, state.scope},
		{fidToolVersion, state.toolVersion},
		{fidClassifierVersion, state.classifierVersion},
	} {
		field, err := encodeStringField(item.id, item.value)
		if err != nil {
			return nil, err
		}
		fields = append(fields, field)
	}
	fields = append(fields, encodeInt64Field(fidDatabaseSchemaVersion, state.databaseSchemaVersion))
	for _, item := range []struct {
		id    uint16
		value string
	}{
		{fidTimezone, state.timezone},
		{fidLogicalFingerprintVersion, state.logicalFingerprintVersion},
		{fidSourceLogicalFingerprint, state.sourceLogicalFingerprint},
		{fidAuditSetDigest, state.auditSetDigest},
		{fidGeneratedAt, state.generatedAt},
	} {
		field, err := encodeStringField(item.id, item.value)
		if err != nil {
			return nil, err
		}
		fields = append(fields, field)
	}
	findingsField, err := encodeFindingsField(fidFindings, state.findings)
	if err != nil {
		return nil, err
	}
	fields = append(fields, findingsField)

	sort.Slice(fields, func(i, j int) bool { return fields[i].id < fields[j].id })

	for _, f := range fields {
		if err := f.write(&buf); err != nil {
			return nil, err
		}
	}

	payload := buf.Bytes()
	// Write ROOT as OBJECT: tag + length + payload
	var result bytes.Buffer
	result.WriteByte(tagOBJECT)
	writeUint64(&result, uint64(len(payload)))
	result.Write(payload)

	return result.Bytes(), nil
}

type tlvField struct {
	id  uint16
	tag uint8
	buf bytes.Buffer
}

func (f *tlvField) write(out *bytes.Buffer) error {
	writeUint16(out, f.id)
	out.WriteByte(f.tag)
	writeUint64(out, uint64(f.buf.Len()))
	out.Write(f.buf.Bytes())
	return nil
}

func encodeInt64Field(id uint16, v int64) tlvField {
	f := tlvField{id: id, tag: tagINT64}
	f.buf.Grow(8)
	writeUint64(&f.buf, uint64(v))
	return f
}

func encodeStringField(id uint16, v string) (tlvField, error) {
	if !utf8.ValidString(v) {
		return tlvField{}, fmt.Errorf("field 0x%04x contains invalid UTF-8", id)
	}
	f := tlvField{id: id, tag: tagSTRING}
	f.buf.WriteString(v)
	return f, nil
}

func encodeBoolField(id uint16, v bool) tlvField {
	f := tlvField{id: id, tag: tagBOOL}
	f.buf.Grow(1)
	if v {
		f.buf.WriteByte(0x01)
	} else {
		f.buf.WriteByte(0x00)
	}
	return f
}

func encodeNullField(id uint16) tlvField {
	return tlvField{id: id, tag: tagNULL}
}

func encodeOptionalStringField(id uint16, o OptionalString) (tlvField, error) {
	if o.IsNull() {
		return encodeNullField(id), nil
	}
	v, ok := o.Value()
	if !ok {
		return encodeNullField(id), nil
	}
	return encodeStringField(id, v)
}

func encodeOptionalInt64Field(id uint16, o OptionalInt64) tlvField {
	if o.IsNull() {
		return encodeNullField(id)
	}
	v, ok := o.Value()
	if !ok {
		return encodeNullField(id)
	}
	return encodeInt64Field(id, v)
}

func encodeOptionalBoolField(id uint16, o OptionalBool) tlvField {
	if o.IsNull() {
		return encodeNullField(id)
	}
	v, ok := o.Value()
	if !ok {
		return encodeNullField(id)
	}
	return encodeBoolField(id, v)
}

func encodeFindingsField(id uint16, findings []authorizedFinding) (tlvField, error) {
	f := tlvField{id: id, tag: tagARRAY}
	var buf bytes.Buffer
	writeUint64(&buf, uint64(len(findings)))
	sorted := append([]authorizedFinding(nil), findings...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].source != sorted[j].source {
			return sorted[i].source < sorted[j].source
		}
		if sorted[i].variant != sorted[j].variant {
			return sorted[i].variant < sorted[j].variant
		}
		if sorted[i].workspaceID != sorted[j].workspaceID {
			return sorted[i].workspaceID < sorted[j].workspaceID
		}
		if sorted[i].sourceRowID != sorted[j].sourceRowID {
			return sorted[i].sourceRowID < sorted[j].sourceRowID
		}
		return sorted[i].auditedField < sorted[j].auditedField
	})
	for _, finding := range sorted {
		fb, err := encodeFindingObject(finding)
		if err != nil {
			return tlvField{}, err
		}
		buf.Write(fb)
	}
	f.buf.Write(buf.Bytes())
	return f, nil
}

func encodeFindingObject(f authorizedFinding) ([]byte, error) {
	var fields []tlvField
	for _, item := range []struct {
		id    uint16
		value string
	}{
		{fidFindingVariant, f.variant},
		{fidFindingSource, f.source},
		{fidFindingWorkspaceID, f.workspaceID},
		{fidFindingSourceRowID, f.sourceRowID},
		{fidFindingAuditedField, f.auditedField},
		{fidFindingReasonCode, f.reasonCode},
		{fidFindingRepairOutcome, f.repairOutcome},
	} {
		field, err := encodeStringField(item.id, item.value)
		if err != nil {
			return nil, err
		}
		fields = append(fields, field)
	}
	fields = append(fields, encodeInt64Field(fidFindingExpectedLegacyValue, f.expectedLegacyValue))
	for _, item := range []struct {
		id    uint16
		value string
	}{
		{fidFindingExpectedCivilState, f.expectedCivilState},
		{fidFindingProposedCivilValue, f.proposedCivilValue},
		{fidFindingTimezone, f.timezone},
		{fidFindingClassifierVersion, f.classifierVersion},
	} {
		field, err := encodeStringField(item.id, item.value)
		if err != nil {
			return nil, err
		}
		fields = append(fields, field)
	}
	txField, err := encodeTransactionOrNull(fidFindingTransactionState, f)
	if err != nil {
		return nil, err
	}
	ruleField, err := encodeRuleOrNull(fidFindingRuleState, f)
	if err != nil {
		return nil, err
	}
	seriesField, err := encodeSeriesOrNull(fidFindingSeriesState, f)
	if err != nil {
		return nil, err
	}
	fields = append(fields, txField, ruleField, seriesField)

	sort.Slice(fields, func(i, j int) bool { return fields[i].id < fields[j].id })

	var payload bytes.Buffer
	for _, ff := range fields {
		if err := ff.write(&payload); err != nil {
			return nil, err
		}
	}
	b := payload.Bytes()
	var result bytes.Buffer
	result.WriteByte(tagOBJECT)
	writeUint64(&result, uint64(len(b)))
	result.Write(b)
	return result.Bytes(), nil
}

func encodeTransactionOrNull(id uint16, f authorizedFinding) (tlvField, error) {
	if !f.hasTransactionState {
		return encodeNullField(id), nil
	}
	return encodeTransactionObject(id, f.transactionState)
}

func encodeTransactionObject(id uint16, ts authorizedTransactionState) (tlvField, error) {
	f := tlvField{id: id, tag: tagOBJECT}
	var fields []tlvField
	for _, item := range []struct {
		id    uint16
		value string
	}{
		{fidTxID, ts.id},
		{fidTxWorkspaceID, ts.workspaceID},
		{fidTxStatus, ts.status},
		{fidTxType, ts.typ},
	} {
		field, err := encodeStringField(item.id, item.value)
		if err != nil {
			return tlvField{}, err
		}
		fields = append(fields, field)
	}
	fields = append(fields, encodeInt64Field(fidTxDateUnix, ts.dateUnix), encodeOptionalInt64Field(fidTxDueDateUnix, ts.dueDateUnix))
	for _, item := range []struct {
		id    uint16
		value OptionalString
	}{
		{fidTxRecurringRuleID, ts.recurringRuleID},
		{fidTxSeriesID, ts.seriesID},
	} {
		field, err := encodeOptionalStringField(item.id, item.value)
		if err != nil {
			return tlvField{}, err
		}
		fields = append(fields, field)
	}
	fields = append(fields, encodeOptionalInt64Field(fidTxRecurrenceSequence, ts.recurrenceSequence), encodeBoolField(fidTxIsRecurringException, ts.isRecurringException))
	reasonField, err := encodeOptionalStringField(fidTxRecurringExceptionReason, ts.recurringExceptionReason)
	if err != nil {
		return tlvField{}, err
	}
	fields = append(fields, reasonField)
	sort.Slice(fields, func(i, j int) bool { return fields[i].id < fields[j].id })
	var payload bytes.Buffer
	for _, ff := range fields {
		ff.write(&payload)
	}
	b := payload.Bytes()
	writeUint64(&f.buf, uint64(len(b)))
	f.buf.Write(b)
	return f, nil
}

func encodeRuleOrNull(id uint16, f authorizedFinding) (tlvField, error) {
	if !f.hasRuleState {
		return encodeNullField(id), nil
	}
	return encodeRuleObject(id, f.ruleState)
}

func encodeRuleObject(id uint16, rs authorizedRuleState) (tlvField, error) {
	f := tlvField{id: id, tag: tagOBJECT}
	var fields []tlvField
	for _, item := range []struct {
		id    uint16
		value string
	}{
		{fidRuleID, rs.id},
		{fidRuleWorkspaceID, rs.workspaceID},
		{fidRuleSeriesID, rs.seriesID},
	} {
		field, err := encodeStringField(item.id, item.value)
		if err != nil {
			return tlvField{}, err
		}
		fields = append(fields, field)
	}
	parentField, err := encodeOptionalStringField(fidRuleParentRuleID, rs.parentRuleID)
	if err != nil {
		return tlvField{}, err
	}
	fields = append(fields, parentField, encodeInt64Field(fidRuleEffectiveStartSequence, rs.effectiveStartSequence), encodeOptionalInt64Field(fidRuleEffectiveEndSequence, rs.effectiveEndSequence), encodeBoolField(fidRuleActive, rs.active))
	sort.Slice(fields, func(i, j int) bool { return fields[i].id < fields[j].id })
	var payload bytes.Buffer
	for _, ff := range fields {
		ff.write(&payload)
	}
	b := payload.Bytes()
	writeUint64(&f.buf, uint64(len(b)))
	f.buf.Write(b)
	return f, nil
}

func encodeSeriesOrNull(id uint16, f authorizedFinding) (tlvField, error) {
	if !f.hasSeriesState {
		return encodeNullField(id), nil
	}
	return encodeSeriesObject(id, f.seriesState)
}

func encodeSeriesObject(id uint16, ss authorizedSeriesState) (tlvField, error) {
	f := tlvField{id: id, tag: tagOBJECT}
	var fields []tlvField
	for _, item := range []struct {
		id    uint16
		value string
	}{
		{fidSeriesID, ss.id},
		{fidSeriesWorkspaceID, ss.workspaceID},
	} {
		field, err := encodeStringField(item.id, item.value)
		if err != nil {
			return tlvField{}, err
		}
		fields = append(fields, field)
	}
	fields = append(fields, encodeBoolField(fidSeriesActive, ss.active))
	sort.Slice(fields, func(i, j int) bool { return fields[i].id < fields[j].id })
	var payload bytes.Buffer
	for _, ff := range fields {
		ff.write(&payload)
	}
	b := payload.Bytes()
	writeUint64(&f.buf, uint64(len(b)))
	f.buf.Write(b)
	return f, nil
}

// ---------- compute ----------

func computeAuthorizedPlanDigestV1(state *authorizedPlanState) ([32]byte, error) {
	if state == nil {
		return [32]byte{}, fmt.Errorf("cannot compute digest for nil state")
	}
	if !state.invariantsValid() {
		return [32]byte{}, fmt.Errorf("state is not valid")
	}
	encoded, err := encodeAuthorizedPlanStateV1(state)
	if err != nil {
		return [32]byte{}, err
	}
	h := sha256.New()
	h.Write([]byte(domainSeparatorV1))
	h.Write(encoded)
	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	return digest, nil
}

// ---------- helpers ----------

func writeUint16(buf *bytes.Buffer, v uint16) {
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], v)
	buf.Write(b[:])
}

func writeUint64(buf *bytes.Buffer, v uint64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	buf.Write(b[:])
}
