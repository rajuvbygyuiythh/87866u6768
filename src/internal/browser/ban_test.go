package browser

import (
	"errors"
	"fmt"
	"testing"
)

// The dash layer relies on errors.As to detect account bans for auto-removal.
func TestBannedErrorUnwraps(t *testing.T) {
	var err error = BannedError{Reason: "server ban signal (phx_close)"}
	err = fmt.Errorf("wrap: %w", err)
	var be BannedError
	if !errors.As(err, &be) {
		t.Fatal("errors.As must find BannedError through wrapping")
	}
	if be.Reason == "" {
		t.Fatal("reason lost")
	}
}

func TestCheckBannedTextLogic(t *testing.T) {
	// CheckBanned needs a live page; here we just guard the error contract.
	e := BannedError{Reason: "account suspended"}
	if e.Error() != "account banned: account suspended" {
		t.Fatalf("unexpected message: %s", e.Error())
	}
}
