package contract

import (
	"strings"
	"testing"
)

func TestAssetSHA256MustBeLowercaseHex(t *testing.T) {
	ok := strings.Repeat("0123456789abcdef", 4)
	bad := map[string]string{
		"traversal": "../../" + strings.Repeat("./", 26) + "secret",
		"uppercase": strings.ToUpper(ok),
		"short":     ok[:63],
		"slash":     ok[:62] + "/a",
	}
	if !ValidSHA256(ok) {
		t.Fatal("valid hash refused")
	}
	for name, sha := range bad {
		if name != "short" && len(sha) != 64 {
			t.Fatalf("%s payload is %d chars", name, len(sha))
		}
		if ValidSHA256(sha) {
			t.Errorf("%s accepted", name)
		}
		a := Asset{ID: "as_a000000000", NodeID: "nd_a000000000", Kind: AssetFile, MIME: "text/plain", Bytes: 1, SHA256: sha}
		if err := (&AssetAttachPayload{Asset: a}).Validate(); err == nil || AsError(err).Field != "asset.sha256" {
			t.Errorf("%s: attach validation: %v", name, err)
		}
	}
	a := Asset{ID: "as_a000000000", NodeID: "nd_a000000000", Kind: AssetFile, MIME: "text/plain", Bytes: 1, SHA256: ok}
	if err := (&AssetAttachPayload{Asset: a}).Validate(); err != nil {
		t.Fatalf("valid attach refused: %v", err)
	}
}

func TestRedactTokens(t *testing.T) {
	secret := "Abc_def-0123456789abcdefghijklmnopqrstuvwxy"
	if len(secret) != 43 {
		t.Fatalf("secret is %d chars", len(secret))
	}
	cases := map[string]string{
		`tool_call mindmap_add {"run_token":"mmr1.38430.` + secret + `"}`: `tool_call mindmap_add {"run_token":"mmr1.38430.[redacted]"}`,
		"export MINDMAP_TOKEN=mmp1.1234." + secret:                        "export MINDMAP_TOKEN=mmp1.1234.[redacted]",
		"open /m/mms1." + secret + "/ now":                                "open /m/mms1.[redacted]/ now",
		"Authorization: Bearer mml1." + secret:                            "Authorization: Bearer mml1.[redacted]",
		"cut short mmr1.38430." + secret[:10]:                             "cut short mmr1.38430.[redacted]",
		"Found 3 pricing pages; v1.2 is out.":                             "Found 3 pricing pages; v1.2 is out.",
		"":                                                                "",
	}
	for in, want := range cases {
		if got := RedactTokens(in); got != want {
			t.Errorf("RedactTokens(%q)\n got %q\nwant %q", in, got, want)
		}
		if got := RedactTokens(want); got != want {
			t.Errorf("RedactTokens is not idempotent on %q: %q", want, got)
		}
	}
}
