package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/oapi-codegen/nullable"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/sketchain/oxalate-bridge/backend/internal/billing"
	"github.com/sketchain/oxalate-bridge/backend/internal/cpaquota"
	"github.com/sketchain/oxalate-bridge/backend/internal/cpaquota/cqsql"
	"github.com/sketchain/oxalate-bridge/backend/internal/db/sqlc"
)

// Window lengths of quota_report_windows.seconds.
const (
	seconds5h = 5 * 3600
	seconds7d = 7 * 24 * 3600
)

func cpaQuotaUnavailable(err error) error {
	return apiErr(http.StatusServiceUnavailable, "cpa_quota_unavailable", err.Error())
}

// cpaQuotaErr maps reader errors to API errors.
func cpaQuotaErr(err error, what string) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, cpaquota.ErrUnavailable):
		return cpaQuotaUnavailable(err)
	case errors.Is(err, cpaquota.ErrNotFound):
		return notFound(what)
	}
	return err
}

// cpaIdentity returns the (instance, auth_index) that locate an account in
// the cpa-quota tables.
func (s *Server) cpaIdentity(ctx context.Context, id openapi_types.UUID) (sqlc.Account, error) {
	row, err := s.Store.Q.GetAccount(ctx, id)
	if err != nil {
		return row, mapDomainErr(err)
	}
	if row.Instance == "" || row.AuthIndex == "" {
		return row, invalidState("account has no cpa-quota instance / auth_index yet")
	}
	if s.CPAQuota == nil {
		return row, cpaQuotaUnavailable(errors.New("cpa-quota reader not configured"))
	}
	return row, nil
}

// AdminGetAccountEstimate implements GET /admin/accounts/{accountId}/estimate.
func (s *Server) AdminGetAccountEstimate(ctx context.Context, req AdminGetAccountEstimateRequestObject) (AdminGetAccountEstimateResponseObject, error) {
	acc, err := s.cpaIdentity(ctx, req.AccountId)
	if err != nil {
		return nil, err
	}
	var e cqsql.Estimate
	err = s.CPAQuota.Read(ctx, func(ctx context.Context, q *cqsql.Queries) error {
		e, err = q.GetEstimate(ctx, cqsql.GetEstimateParams{Instance: acc.Instance, AuthIndex: acc.AuthIndex})
		return err
	})
	if err != nil {
		return nil, cpaQuotaErr(err, "cpa-quota estimate")
	}
	return AdminGetAccountEstimate200JSONResponse{Instance: e.Instance, AuthIndex: e.AuthIndex,
		Tier: e.Tier, State: e.State, Basis: e.Basis, K: nullFloat(e.K), KSource: e.KSource, TotalsFrom: e.TotalsFrom, SafetyFactor: e.SafetyFactor,
		Used5h: e.Used5h, Reset5h: nullTime(e.Reset5h), Used7d: e.Used7d, Reset7d: nullTime(e.Reset7d),
		FiveHourTotalUsd: e.FiveHourTotalUsd, WeeklyTotalUsd: e.WeeklyTotalUsd, RemainingFiveHourUsd: e.RemainingFiveHourUsd,
		RemainingWeekUsd: e.RemainingWeekUsd, CleanBlocks: int(e.CleanBlocks), TotalBlocks: int(e.TotalBlocks),
		Segments: int(e.Segments), ExcludedSegments: int(e.ExcludedSegments), Ratio: e.Ratio, ObservedAt: nullTime(e.ObservedAt),
		EstimatedAt: e.EstimatedAt.UTC(), Detail: e.Detail}, nil
}

// AdminGetAccountKTrend implements GET /admin/accounts/{accountId}/k-trend.
func (s *Server) AdminGetAccountKTrend(ctx context.Context, req AdminGetAccountKTrendRequestObject) (AdminGetAccountKTrendResponseObject, error) {
	days := 30
	if req.Params.Days != nil {
		days = *req.Params.Days
	}
	if days < 1 || days > 365 {
		return nil, badRequest("days must be within 1..365")
	}
	acc, err := s.cpaIdentity(ctx, req.AccountId)
	if err != nil {
		return nil, err
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	since := today.AddDate(0, 0, -(days - 1))
	var rows []cqsql.ListKTrendRow
	err = s.CPAQuota.Read(ctx, func(ctx context.Context, q *cqsql.Queries) error {
		rows, err = q.ListKTrend(ctx, cqsql.ListKTrendParams{Instance: acc.Instance, AuthIndex: acc.AuthIndex, Since: since})
		return err
	})
	if err != nil {
		return nil, cpaQuotaErr(err, "k trend")
	}
	items := make([]KTrendDay, len(rows))
	for i, r := range rows {
		items[i] = KTrendDay{Day: openapi_types.Date{Time: r.Day}, Gain5h: r.Gain5h, Gain7d: r.Gain7d, K: nullFloat(r.K),
			Segments: int(r.Segments), ExcludedSegments: int(r.ExcludedSegments), CostUsd: r.CostUsd, Gain: r.Gain,
			UpdatedAt: r.UpdatedAt.UTC()}
	}
	return AdminGetAccountKTrend200JSONResponse{Days: days, Items: items}, nil
}

// AdminGetAccountQuotaSeries implements GET /admin/accounts/{accountId}/quota-series.
func (s *Server) AdminGetAccountQuotaSeries(ctx context.Context, req AdminGetAccountQuotaSeriesRequestObject) (AdminGetAccountQuotaSeriesResponseObject, error) {
	p := req.Params
	var seconds int64
	def := 7 * 24 * time.Hour
	switch p.Window {
	case AdminGetAccountQuotaSeriesParamsWindowN5h:
		seconds = seconds5h
	case AdminGetAccountQuotaSeriesParamsWindowN7d:
		seconds, def = seconds7d, 30*24*time.Hour
	default:
		return nil, badRequest("window must be 5h or 7d")
	}
	to := time.Now().UTC()
	if p.To != nil {
		to = p.To.UTC()
	}
	from := to.Add(-def)
	if p.From != nil {
		from = p.From.UTC()
	}
	if !to.After(from) {
		return nil, badRequest("to must be after from")
	}
	if to.Sub(from) > 400*24*time.Hour {
		return nil, badRequest("range too large (max 400 days)")
	}
	acc, err := s.cpaIdentity(ctx, req.AccountId)
	if err != nil {
		return nil, err
	}
	series, err := s.CPAQuota.WindowSeries(ctx, acc.Instance, acc.AuthIndex, seconds, from, to)
	if err != nil {
		return nil, cpaQuotaErr(err, "quota series")
	}
	items := make([]QuotaPoint, len(series.Points))
	for i, pt := range series.Points {
		items[i] = QuotaPoint{At: pt.At, UsedPercent: pt.UsedPercent, ResetAt: nullTime(pt.ResetAt)}
	}
	out := QuotaSeries{Window: QuotaSeriesWindow(p.Window), From: from, To: to, Downsampled: series.Downsampled,
		TotalPoints: series.Total, Items: items, BucketSeconds: nullable.NewNullNullable[int64]()}
	if series.Downsampled {
		out.BucketSeconds = nullable.NewNullableWithValue(int64(series.BucketWidth.Seconds()))
	}
	return AdminGetAccountQuotaSeries200JSONResponse(out), nil
}

// AdminGetAccountCostCompare implements GET /admin/accounts/{accountId}/cost-compare.
func (s *Server) AdminGetAccountCostCompare(ctx context.Context, req AdminGetAccountCostCompareRequestObject) (AdminGetAccountCostCompareResponseObject, error) {
	p := req.Params
	bucket := "hour"
	if p.Bucket != nil {
		bucket = string(*p.Bucket)
	}
	if bucket != "hour" && bucket != "day" {
		return nil, badRequest("bucket must be hour or day")
	}
	from, to := p.From.UTC(), p.To.UTC()
	if !to.After(from) {
		return nil, badRequest("to must be after from")
	}
	if to.Sub(from) > 400*24*time.Hour {
		return nil, badRequest("range too large (max 400 days)")
	}
	acc, err := s.cpaIdentity(ctx, req.AccountId)
	if err != nil {
		return nil, err
	}
	var cpaRows []cqsql.CostByBucketRow
	err = s.CPAQuota.Read(ctx, func(ctx context.Context, q *cqsql.Queries) error {
		cpaRows, err = q.CostByBucket(ctx, cqsql.CostByBucketParams{Instance: acc.Instance, AuthIndex: acc.AuthIndex,
			Bucket: bucket, FromNs: cpaquota.NsOf(from), ToNs: cpaquota.NsOf(to)})
		return err
	})
	if err != nil {
		return nil, cpaQuotaErr(err, "cpa-quota requests")
	}
	ourRows, err := s.Store.Q.AccountCostByBucket(ctx, sqlc.AccountCostByBucketParams{AccountID: acc.ID, Bucket: bucket, FromAt: from, ToAt: to})
	if err != nil {
		return nil, err
	}
	out := CostCompare{Bucket: CostCompareBucket(bucket), From: from, To: to, Items: MergeCostBuckets(cpaRows, ourRows)}
	for _, it := range out.Items {
		out.CpaQuotaTotalMicro += it.CpaQuotaCostMicro
		out.BridgeTotalMicro += it.BridgeUpstreamCostMicro
	}
	out.DiffMicro = out.CpaQuotaTotalMicro - out.BridgeTotalMicro
	return AdminGetAccountCostCompare200JSONResponse(out), nil
}

// MergeCostBuckets joins both sides by bucket (both inputs are ordered by
// bucket); a bucket missing on one side counts 0 there.
func MergeCostBuckets(cpa []cqsql.CostByBucketRow, ours []sqlc.AccountCostByBucketRow) []CostCompareRow {
	out := make([]CostCompareRow, 0, max(len(cpa), len(ours)))
	i, j := 0, 0
	for i < len(cpa) || j < len(ours) {
		var row CostCompareRow
		switch {
		case j >= len(ours) || (i < len(cpa) && cpa[i].Bucket.Before(ours[j].Bucket)):
			row = CostCompareRow{Bucket: cpa[i].Bucket.UTC(), CpaQuotaCostMicro: billing.USDToMicro(cpa[i].CostUsd), CpaQuotaRequests: cpa[i].Requests}
			i++
		case i >= len(cpa) || ours[j].Bucket.Before(cpa[i].Bucket):
			row = CostCompareRow{Bucket: ours[j].Bucket.UTC(), BridgeUpstreamCostMicro: ours[j].UpstreamCostMicro, BridgeRequests: ours[j].Requests}
			j++
		default:
			row = CostCompareRow{Bucket: cpa[i].Bucket.UTC(), CpaQuotaCostMicro: billing.USDToMicro(cpa[i].CostUsd), CpaQuotaRequests: cpa[i].Requests,
				BridgeUpstreamCostMicro: ours[j].UpstreamCostMicro, BridgeRequests: ours[j].Requests}
			i++
			j++
		}
		row.DiffMicro = row.CpaQuotaCostMicro - row.BridgeUpstreamCostMicro
		out = append(out, row)
	}
	return out
}
