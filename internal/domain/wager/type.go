package wager

// Type identifies the kind of wagering transaction.
type Type string

const (
	TypeOpening  Type = "OPENING"
	TypeBet      Type = "BET"
	TypeWin      Type = "WIN"
	TypeLoss     Type = "LOSS"
	TypeRefund   Type = "REFUND"
	TypeRollback Type = "ROLLBACK"
)

// IsExternal reports whether the type may be submitted by an external provider.
func (t Type) IsExternal() bool {
	switch t {
	case TypeBet, TypeWin, TypeLoss, TypeRefund, TypeRollback:
		return true
	default:
		return false
	}
}

func (t Type) valid() bool {
	switch t {
	case TypeOpening, TypeBet, TypeWin, TypeLoss, TypeRefund, TypeRollback:
		return true
	default:
		return false
	}
}
