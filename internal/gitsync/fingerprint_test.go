package gitsync

import "testing"

const lsRemoteSample = "9f1a2b3c4d5e6f708192a3b4c5d6e7f809a1b2c3\trefs/heads/main\n" +
	"1122334455667788990011223344556677889900\trefs/heads/develop\n" +
	"aabbccddeeff00112233445566778899aabbccdd\trefs/tags/v1.0.0\n"

func TestParseLsRemote(t *testing.T) {
	refs, err := ParseLsRemote(lsRemoteSample)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(refs) != 3 {
		t.Fatalf("got %d refs, want 3", len(refs))
	}
	if refs[0].SHA != "9f1a2b3c4d5e6f708192a3b4c5d6e7f809a1b2c3" {
		t.Errorf("SHA = %q", refs[0].SHA)
	}
	if refs[0].Name != "refs/heads/main" {
		t.Errorf("Name = %q", refs[0].Name)
	}
}

func TestParseLsRemoteEmpty(t *testing.T) {
	refs, err := ParseLsRemote("")
	if err != nil {
		t.Fatalf("parse empty: %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("got %d refs, want 0", len(refs))
	}
}

func TestParseLsRemoteSkipsBlankLines(t *testing.T) {
	refs, err := ParseLsRemote("\n" + lsRemoteSample + "\n\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(refs) != 3 {
		t.Fatalf("got %d refs, want 3", len(refs))
	}
}

func TestParseLsRemoteRejectsMalformedLine(t *testing.T) {
	if _, err := ParseLsRemote("not-a-ref-line\n"); err == nil {
		t.Fatal("expected error for a line with no tab separator")
	}
}

func TestFingerprintIsOrderIndependent(t *testing.T) {
	a := []Ref{
		{Name: "refs/heads/main", SHA: "aaa"},
		{Name: "refs/tags/v1", SHA: "bbb"},
	}
	b := []Ref{
		{Name: "refs/tags/v1", SHA: "bbb"},
		{Name: "refs/heads/main", SHA: "aaa"},
	}
	if Fingerprint(a) != Fingerprint(b) {
		t.Fatal("fingerprint must not depend on ref order")
	}
}

func TestFingerprintChangesWithContent(t *testing.T) {
	base := []Ref{{Name: "refs/heads/main", SHA: "aaa"}}

	newSHA := []Ref{{Name: "refs/heads/main", SHA: "bbb"}}
	if Fingerprint(base) == Fingerprint(newSHA) {
		t.Error("a moved branch must change the fingerprint")
	}

	added := []Ref{
		{Name: "refs/heads/main", SHA: "aaa"},
		{Name: "refs/heads/next", SHA: "ccc"},
	}
	if Fingerprint(base) == Fingerprint(added) {
		t.Error("a new branch must change the fingerprint")
	}

	if Fingerprint(nil) == Fingerprint(base) {
		t.Error("an empty ref set must differ from a populated one")
	}
}

func TestFingerprintIsStable(t *testing.T) {
	refs := []Ref{{Name: "refs/heads/main", SHA: "aaa"}}
	if Fingerprint(refs) != Fingerprint(refs) {
		t.Fatal("fingerprint must be deterministic")
	}
}

func TestFingerprintIsInjectiveAcrossFieldBoundary(t *testing.T) {
	// Without length prefixing these two distinct ref sets serialize to the
	// same bytes and collide.
	a := []Ref{{SHA: "aaa", Name: "x\ty"}}
	b := []Ref{{SHA: "aaa\tx", Name: "y"}}
	if Fingerprint(a) == Fingerprint(b) {
		t.Fatal("distinct ref sets must not share a fingerprint")
	}
}

func TestParseLsRemoteKeepsTabsInRefName(t *testing.T) {
	// git cuts at the first tab, so any later tab belongs to the ref name.
	refs, err := ParseLsRemote("aabbcc\trefs/heads/odd\tname\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("got %d refs, want 1", len(refs))
	}
	if refs[0].SHA != "aabbcc" {
		t.Errorf("SHA = %q, want aabbcc", refs[0].SHA)
	}
	if refs[0].Name != "refs/heads/odd\tname" {
		t.Errorf("Name = %q, want the full remainder including the tab", refs[0].Name)
	}
}
