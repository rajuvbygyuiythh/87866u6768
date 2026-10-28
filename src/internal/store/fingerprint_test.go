package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveProfile(t *testing.T) {
	root := t.TempDir()
	st := &Store{Dir: filepath.Join(root, "data")}
	dir := st.ProfileDir("uid123")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if !st.HasProfile("uid123") {
		t.Fatal("profile should exist")
	}
	if err := st.RemoveProfile("uid123"); err != nil {
		t.Fatal(err)
	}
	if st.HasProfile("uid123") {
		t.Fatal("profile should be gone")
	}
	// removing again is a no-op
	if err := st.RemoveProfile("uid123"); err != nil {
		t.Fatal(err)
	}
}

func TestFingerprintRoundTripAndUA(t *testing.T) {
	root := t.TempDir()
	st := &Store{Dir: filepath.Join(root, "data")}
	_ = os.MkdirAll(st.Dir, 0o755)

	// default UA without fingerprint
	if ua := st.UserAgentFor("x"); ua != DefaultUA {
		t.Fatalf("want default UA, got %s", ua)
	}
	fp := Fingerprint{UserAgent: UAPool[3], Language: "en-US", ScreenResolution: "1920x1080", TimeZone: "Asia/Dhaka", HardwareConcurrency: "8"}
	if !st.SaveFingerprint("uid9", fp) {
		t.Fatal("save failed")
	}
	got, ok := st.LoadFingerprint("uid9")
	if !ok || got.UserAgent != fp.UserAgent {
		t.Fatalf("round trip failed: %+v ok=%v", got, ok)
	}
	if ua := st.UserAgentFor("uid9"); ua != fp.UserAgent {
		t.Fatalf("UserAgentFor must replay the stored UA, got %s", ua)
	}
	if SecCHUA(fp.UserAgent) == "" || ChromeMajor(fp.UserAgent) == "" {
		t.Fatal("headers helpers empty")
	}
}
