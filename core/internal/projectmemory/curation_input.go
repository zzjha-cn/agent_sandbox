package projectmemory

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

// DecodeDraftRequest validates raw input before typed decoding can discard
// duplicate keys, nulls or invalid UTF-8. No storage or model access occurs.
func DecodeDraftRequest(raw []byte) (DraftRequest, error) {
	var request DraftRequest
	if len(raw) > 128<<10 || !utf8.Valid(raw) {
		return request, errors.New("invalid or oversized draft request")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if err := checkJSON(d, 0); err != nil {
		return request, err
	}
	if _, err := d.Token(); err != io.EOF {
		return request, errors.New("trailing draft input")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&request); err != nil {
		return request, err
	}
	return request, request.Validate()
}
