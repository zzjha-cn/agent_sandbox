package execution

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"testing"
)

func TestTimeoutNormalizationMatchesExactRationalValues(t *testing.T) {
	limit := big.NewRat(86400, 1)
	for _, coefficient := range []int{0, 1, 10, 12, 99, 100, 1001, 86400, 86401} {
		for fraction := 0; fraction <= 5; fraction++ {
			mantissa := strconv.Itoa(coefficient)
			if fraction > 0 {
				if len(mantissa) <= fraction {
					mantissa = strings.Repeat("0", fraction-len(mantissa)+1) + mantissa
				}
				i := len(mantissa) - fraction
				mantissa = mantissa[:i] + "." + mantissa[i:]
			}
			for exponent := -6; exponent <= 6; exponent++ {
				for _, sign := range []string{"", "-"} {
					literal := fmt.Sprintf("%s%se%d", sign, mantissa, exponent)
					exact, ok := new(big.Rat).SetString(literal)
					if !ok {
						t.Fatal("invalid test number", literal)
					}
					valid := exact.IsInt() && exact.Sign() >= 0 && exact.Cmp(limit) <= 0
					value, err := taskInteger(json.RawMessage(literal))
					if (err == nil) != valid || (valid && int64(value) != exact.Num().Int64()) {
						t.Fatalf("%s: value=%d err=%v exact=%s", literal, value, err, exact)
					}
				}
			}
		}
	}
}

func TestTaskDecoderRejectsAmbiguousNestedAuthority(t *testing.T) {
	base := `{"schema_version":"sbx.execution/v2","task_id":"job","prompt":"x"`
	for _, suffix := range []string{
		`,"agent":{"provider":"claude","provider":"codex"}}`,
		`,"agent":{"Provider":"codex"}}`,
		`,"agent":{"provider":"codex"},"source":null}`,
		`,"agent":{"provider":"codex"},"limits":{"timeout_seconds":1,"timeout_seconds":2}}`,
		`,"agent":{"provider":"codex"},"tools":[{"id":"r","kind":"records.get","grant_ref":"g","targets":[null],"fields":["status"]}]}`,
		`,"agent":{"provider":"codex"},"operations":[{"id":"x","grant_ref":"g","target":"r","value":{"status":"a","status":"b"}}]}`,
	} {
		_, err := DecodeTaskSpec(strings.NewReader(base + suffix))
		if err == nil || !(strings.Contains(err.Error(), "duplicate task field") || strings.Contains(err.Error(), "unknown task field") || strings.Contains(err.Error(), "must not be null")) {
			t.Fatal("ambiguous authority not rejected at decoding", suffix, err)
		}
	}
	if _, err := DecodeTaskSpec(strings.NewReader(`{"schema_version":"sbx.execution/v1","task_id":"job","prompt":"x"}`)); err == nil {
		t.Fatal("v1 accepted")
	}
}
