package proxy

import (
	"time"

	"github.com/google/uuid"

	"github.com/sketchain/oxalate-bridge/backend/internal/db/sqlc"
)

func sqlcGetPeriod(sub uuid.UUID, start time.Time) sqlc.GetPeriodParams {
	return sqlc.GetPeriodParams{SubscriptionID: sub, PeriodStart: start}
}

func sqlcSetAccountError(id uuid.UUID, reason string) sqlc.SetAccountErrorParams {
	return sqlc.SetAccountErrorParams{ID: id, ErrorReason: reason}
}
