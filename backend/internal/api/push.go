package api

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/sketchain/oxalate-bridge/backend/internal/db/sqlc"
	"github.com/sketchain/oxalate-bridge/backend/internal/notify"
)

// Web Push devices, notification preferences and the user's locale.

func pushDeviceOf(d sqlc.PushSubscription) PushDevice {
	return PushDevice{Id: d.ID, Endpoint: d.Endpoint, DeviceLabel: d.DeviceLabel, CreatedAt: d.CreatedAt,
		LastSuccessAt: nullTime(d.LastSuccessAt), Failures: int(d.Failures)}
}

func (s *Server) pushEnabled() error {
	if s.Notify == nil || s.Notify.PublicKey() == "" {
		return apiErr(http.StatusServiceUnavailable, "push_unavailable", "push notifications are not available")
	}
	return nil
}

// GetPushConfig implements GET /push/config.
func (s *Server) GetPushConfig(ctx context.Context, _ GetPushConfigRequestObject) (GetPushConfigResponseObject, error) {
	if err := s.pushEnabled(); err != nil {
		return nil, err
	}
	return GetPushConfig200JSONResponse(PushConfig{PublicKey: s.Notify.PublicKey()}), nil
}

// ListPushSubscriptions implements GET /push/subscriptions.
func (s *Server) ListPushSubscriptions(ctx context.Context, _ ListPushSubscriptionsRequestObject) (ListPushSubscriptionsResponseObject, error) {
	rows, err := s.Store.Q.ListPushSubscriptions(ctx, currentUser(ctx).ID)
	if err != nil {
		return nil, err
	}
	items := make([]PushDevice, len(rows))
	for i, r := range rows {
		items[i] = pushDeviceOf(r)
	}
	return ListPushSubscriptions200JSONResponse(PushDeviceList{Items: items}), nil
}

// CreatePushSubscription implements POST /push/subscriptions.
func (s *Server) CreatePushSubscription(ctx context.Context, req CreatePushSubscriptionRequestObject) (CreatePushSubscriptionResponseObject, error) {
	if err := s.pushEnabled(); err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, badRequest("body required")
	}
	b := req.Body
	if len(b.Endpoint) > 2048 || b.Keys.P256dh == "" || b.Keys.Auth == "" || len(b.Keys.P256dh) > 256 || len(b.Keys.Auth) > 256 {
		return nil, badRequest("invalid subscription")
	}
	if err := s.Notify.ValidEndpoint(b.Endpoint); err != nil {
		return nil, badRequest(err.Error())
	}
	id, _ := uuid.NewV7()
	row, err := s.Store.Q.UpsertPushSubscription(ctx, sqlc.UpsertPushSubscriptionParams{ID: id, UserID: currentUser(ctx).ID,
		Endpoint: b.Endpoint, P256dh: b.Keys.P256dh, Auth: b.Keys.Auth, DeviceLabel: notify.DeviceLabel(userAgent(ctx))})
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return CreatePushSubscription201JSONResponse(pushDeviceOf(row)), nil
}

// DeletePushSubscription implements DELETE /push/subscriptions/{id}.
func (s *Server) DeletePushSubscription(ctx context.Context, req DeletePushSubscriptionRequestObject) (DeletePushSubscriptionResponseObject, error) {
	n, err := s.Store.Q.DeletePushSubscription(ctx, sqlc.DeletePushSubscriptionParams{ID: req.PushSubscriptionId, UserID: currentUser(ctx).ID})
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, notFound("device")
	}
	return DeletePushSubscription204Response{}, nil
}

// SendTestPush implements POST /push/test.
func (s *Server) SendTestPush(ctx context.Context, _ SendTestPushRequestObject) (SendTestPushResponseObject, error) {
	if err := s.pushEnabled(); err != nil {
		return nil, err
	}
	u, err := s.Store.Q.GetUser(ctx, currentUser(ctx).ID)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if s.rateHit(ctx, "pushtest", u.ID.String(), 10) {
		return nil, apiErr(http.StatusTooManyRequests, "rate_limited", "too many test notifications")
	}
	n, err := s.Notify.SendTest(ctx, u.ID, u.Locale)
	if err != nil {
		return nil, err
	}
	return SendTestPush200JSONResponse(PushTestResult{Devices: n}), nil
}

func (s *Server) notificationPrefs(ctx context.Context) (NotificationPrefs, error) {
	u := currentUser(ctx)
	prefs, threshold, err := s.Notify.Prefs(ctx, u.ID, u.Role)
	if err != nil {
		return NotificationPrefs{}, err
	}
	items := make([]NotificationPref, len(prefs))
	for i, p := range prefs {
		items[i] = NotificationPref{Event: NotificationEvent(p.Event), Group: NotificationPrefGroup(p.Group),
			Enabled: p.Enabled, DefaultEnabled: p.DefaultEnabled}
	}
	return NotificationPrefs{Items: items, BalanceThresholdMicro: threshold}, nil
}

// GetNotificationPrefs implements GET /notifications/prefs.
func (s *Server) GetNotificationPrefs(ctx context.Context, _ GetNotificationPrefsRequestObject) (GetNotificationPrefsResponseObject, error) {
	if s.Notify == nil {
		return nil, s.pushEnabled()
	}
	out, err := s.notificationPrefs(ctx)
	if err != nil {
		return nil, err
	}
	return GetNotificationPrefs200JSONResponse(out), nil
}

// UpdateNotificationPrefs implements PUT /notifications/prefs.
func (s *Server) UpdateNotificationPrefs(ctx context.Context, req UpdateNotificationPrefsRequestObject) (UpdateNotificationPrefsResponseObject, error) {
	if s.Notify == nil {
		return nil, s.pushEnabled()
	}
	if req.Body == nil {
		return nil, badRequest("body required")
	}
	u := currentUser(ctx)
	enabled := map[string]bool{}
	if req.Body.Items != nil {
		for _, it := range *req.Body.Items {
			sp, ok := notify.SpecOf(string(it.Event))
			if !ok {
				return nil, badRequest("unknown event " + string(it.Event))
			}
			if !notify.Visible(sp, u.Role) {
				return nil, forbidden("event " + sp.Event + " is for admins")
			}
			enabled[sp.Event] = it.Enabled
		}
	}
	th := req.Body.BalanceThresholdMicro
	if th != nil && (*th < 0 || *th > 1_000_000_000_000) {
		return nil, badRequest("balance_threshold_micro out of range")
	}
	if err := s.Notify.SetPrefs(ctx, u.ID, u.Role, enabled, th); err != nil {
		return nil, err
	}
	out, err := s.notificationPrefs(ctx)
	if err != nil {
		return nil, err
	}
	return UpdateNotificationPrefs200JSONResponse(out), nil
}

// SetMyLocale implements PUT /me/locale.
func (s *Server) SetMyLocale(ctx context.Context, req SetMyLocaleRequestObject) (SetMyLocaleResponseObject, error) {
	if req.Body == nil {
		return nil, badRequest("body required")
	}
	if req.Body.Locale != "zh-CN" && req.Body.Locale != "en" {
		return nil, badRequest("locale must be zh-CN or en")
	}
	if err := s.Store.Q.SetUserLocale(ctx, sqlc.SetUserLocaleParams{ID: currentUser(ctx).ID, Locale: string(req.Body.Locale)}); err != nil {
		return nil, err
	}
	return SetMyLocale204Response{}, nil
}
