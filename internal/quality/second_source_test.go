package quality

import (
	"strings"
	"testing"
)

func TestParseVerificationResult_PlainToken(t *testing.T) {
	raw := "RESULT: CONFIRMED\nVALUE: "
	res, val, ok := ParseVerificationResult(raw)
	if !ok {
		t.Fatalf("expected ok=true, got ok=false")
	}
	if res != ResultConfirmed {
		t.Errorf("expected ResultConfirmed, got %v", res)
	}
	if val != "" {
		t.Errorf("expected empty value, got %q", val)
	}
}

func TestParseVerificationResult_BracketedToken(t *testing.T) {
	raw := "RESULT: [CONFIRMED]\nVALUE: []"
	res, _, ok := ParseVerificationResult(raw)
	if !ok {
		t.Fatalf("expected ok=true, got ok=false")
	}
	if res != ResultConfirmed {
		t.Errorf("expected ResultConfirmed, got %v", res)
	}
}

func TestParseVerificationResult_MarkdownBold(t *testing.T) {
	raw := "**RESULT:** CONTRADICTED\n**VALUE:** $120,000"
	res, val, ok := ParseVerificationResult(raw)
	if !ok {
		t.Fatalf("expected ok=true, got ok=false")
	}
	if res != ResultContradicted {
		t.Errorf("expected ResultContradicted, got %v", res)
	}
	if !strings.Contains(val, "$120,000") {
		t.Errorf("expected value containing '$120,000', got %q", val)
	}
}

func TestParseVerificationResult_LowercaseAndWhitespace(t *testing.T) {
	raw := "   result:   unclear   \n   value:   "
	res, _, ok := ParseVerificationResult(raw)
	if !ok {
		t.Fatalf("expected ok=true, got ok=false")
	}
	if res != ResultUnclear {
		t.Errorf("expected ResultUnclear, got %v", res)
	}
}

func TestParseVerificationResult_FallbackToKeywords(t *testing.T) {
	raw := "Based on the evidence analyzed above, the statement is CONTRADICTED.\nThe current CEO is John Doe."
	res, _, ok := ParseVerificationResult(raw)
	if !ok {
		t.Fatalf("expected ok=true, got ok=false")
	}
	if res != ResultContradicted {
		t.Errorf("expected ResultContradicted, got %v", res)
	}
}

func TestParseVerificationResult_GarbageFallsBackUnclearWithFalseOk(t *testing.T) {
	raw := "I am an AI assistant and I cannot determine this. Please look elsewhere for details."
	res, _, ok := ParseVerificationResult(raw)
	if ok {
		t.Errorf("expected ok=false for unparseable output, got ok=true")
	}
	if res != ResultUnclear {
		t.Errorf("expected ResultUnclear fallback, got %v", res)
	}
}

func TestBuildVerificationQuery_RoleVsVersion(t *testing.T) {
	detector := NewEntityDetector()

	// Executive claim
	execClaim := "Tim Cook is the CEO of Apple"
	execEntity := detector.Detect(execClaim)
	execQuery := BuildVerificationQuery(execEntity)

	if strings.Contains(execQuery, "current latest version") {
		t.Errorf("execQuery unexpectedly contains version template: %q", execQuery)
	}
	if !strings.Contains(execQuery, "CEO of Apple") {
		t.Errorf("execQuery does not contain 'CEO of Apple': %q", execQuery)
	}

	// Price claim
	priceClaim := "Bitcoin current price is $95,000"
	priceEntity := detector.Detect(priceClaim)
	priceQuery := BuildVerificationQuery(priceEntity)

	if strings.Contains(priceQuery, "current latest version") {
		t.Errorf("priceQuery unexpectedly contains version template: %q", priceQuery)
	}
	if !strings.Contains(priceQuery, "Bitcoin") {
		t.Errorf("priceQuery does not contain 'Bitcoin': %q", priceQuery)
	}
}
