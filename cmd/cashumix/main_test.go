package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/apeirs/cashumix/cashu"
)

func TestReadInputAndGroups(t *testing.T) {
	raw, err := os.ReadFile("../../cashu/testdata/v3.txt")
	if err != nil {
		t.Fatal(err)
	}
	// A file whose name starts with "cashu" is still read as a file.
	dir := t.TempDir()
	path := filepath.Join(dir, "cashuA-token.txt")
	os.WriteFile(path, raw, 0o600)
	for _, in := range []string{path, string(raw)} {
		tok, err := readInput(in)
		if err != nil || tok.Amount() != 10 {
			t.Fatalf("readInput(%.30q): %v", in, err)
		}
	}
	if _, err := readInput(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing file accepted")
	}

	tok, _ := cashu.ParseToken(string(raw))
	gs, err := groups(tok, "")
	if err != nil || len(gs) != 1 || gs[0].mint.String() != "https://8333.space:3338" || len(gs[0].proofs) != 2 {
		t.Fatalf("groups from token: %+v %v", gs, err)
	}
	gs, err = groups(tok, "testnut.cashu.space")
	if err != nil || gs[0].mint.String() != "https://testnut.cashu.space" {
		t.Fatalf("explicit mint: %+v %v", gs, err)
	}
	noMint, _ := cashu.ParseInput([]byte(`[{"amount":1,"id":"00","secret":"s","C":"02"}]`))
	if _, err := groups(noMint, ""); err == nil {
		t.Fatal("proofs without a mint accepted")
	}
	v4, _ := os.ReadFile("../../cashu/testdata/v4-single.txt")
	local, _ := cashu.ParseToken(string(v4)) // mint is http://localhost:3338
	if _, err := groups(local, ""); err == nil {
		t.Fatal("http mint accepted")
	}
}

func TestGatewayTokenMintIsUnwrapped(t *testing.T) {
	tok := &cashu.Token{Entries: []cashu.TokenEntry{{Mint: "http://127.0.0.1:3338/testnut.cashu.space",
		Proofs: []cashu.Proof{{Amount: 1, ID: "00ad268c4d1f5826", Secret: "s", C: "02aa"}}}}}
	gs, err := groups(tok, "")
	if err != nil || gs[0].mint.String() != "https://testnut.cashu.space" {
		t.Fatalf("%+v %v", gs, err)
	}
}
