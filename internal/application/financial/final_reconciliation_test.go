package financial

import (
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

func TestReconciliationChecksOptionalWinReference(t *testing.T) {
	ref := auditFixture().Transactions[1]
	win := ref
	win.ID, win.ExternalID, win.Type, win.ReferenceExternalID = "win", "win", wager.TypeWin, "bet"
	refs := map[[2]string]ports.AuditTransaction{{"provider", "bet"}: ref}
	if !expectedMovement(win, refs).valid {
		t.Fatal("valid win reference rejected")
	}
	win.RoundID = "foreign-round"
	if expectedMovement(win, refs).valid {
		t.Fatal("corrupt win reference not detected")
	}
	win.RoundID = ref.RoundID
	delete(refs, [2]string{"provider", "bet"})
	if expectedMovement(win, refs).valid {
		t.Fatal("missing win reference not detected")
	}
}
