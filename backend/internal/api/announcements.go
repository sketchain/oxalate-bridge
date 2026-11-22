package api

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/sketchain/oxalate-bridge/backend/internal/db/sqlc"
)

func announcementOf(a sqlc.Announcement, now time.Time) Announcement {
	active := a.Enabled && (a.StartsAt == nil || !now.Before(*a.StartsAt)) && (a.EndsAt == nil || now.Before(*a.EndsAt))
	return Announcement{Id: a.ID, Title: a.Title, Body: a.Body, Level: AnnouncementLevel(a.Level), StartsAt: nullTime(a.StartsAt),
		EndsAt: nullTime(a.EndsAt), Enabled: a.Enabled, Active: active, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt}
}

type announcementFields struct {
	title, body, level string
	starts, ends       *time.Time
	enabled            bool
}

func announcementInput(in *AnnouncementInput) (announcementFields, error) {
	if in == nil {
		return announcementFields{}, badRequest("body required")
	}
	f := announcementFields{title: strings.TrimSpace(in.Title), body: in.Body, level: string(in.Level), enabled: in.Enabled,
		starts: optTime(in.StartsAt, nil), ends: optTime(in.EndsAt, nil)}
	switch {
	case f.title == "" || len(f.title) > 200:
		return f, badRequest("title must have 1-200 characters")
	case len(f.body) > 5000:
		return f, badRequest("body too long (max 5000)")
	case f.level != string(Info) && f.level != string(Warning):
		return f, badRequest("level must be info or warning")
	case f.starts != nil && f.ends != nil && !f.ends.After(*f.starts):
		return f, badRequest("ends_at must be after starts_at")
	}
	return f, nil
}

// ListActiveAnnouncements implements GET /announcements.
func (s *Server) ListActiveAnnouncements(ctx context.Context, _ ListActiveAnnouncementsRequestObject) (ListActiveAnnouncementsResponseObject, error) {
	now := time.Now().UTC()
	rows, err := s.Store.Q.ListActiveAnnouncements(ctx, sqlc.ListActiveAnnouncementsParams{Now: now, UserID: currentUser(ctx).ID})
	if err != nil {
		return nil, err
	}
	items := make([]Announcement, len(rows))
	for i, a := range rows {
		items[i] = announcementOf(a, now)
	}
	return ListActiveAnnouncements200JSONResponse{Items: items}, nil
}

// DismissAnnouncement implements POST /announcements/{id}/dismiss.
func (s *Server) DismissAnnouncement(ctx context.Context, req DismissAnnouncementRequestObject) (DismissAnnouncementResponseObject, error) {
	if _, err := s.Store.Q.GetAnnouncement(ctx, req.AnnouncementId); err != nil {
		return nil, mapDomainErr(err)
	}
	if err := s.Store.Q.DismissAnnouncement(ctx, sqlc.DismissAnnouncementParams{AnnouncementID: req.AnnouncementId, UserID: currentUser(ctx).ID}); err != nil {
		return nil, err
	}
	return DismissAnnouncement204Response{}, nil
}

// AdminListAnnouncements implements GET /admin/announcements.
func (s *Server) AdminListAnnouncements(ctx context.Context, _ AdminListAnnouncementsRequestObject) (AdminListAnnouncementsResponseObject, error) {
	rows, err := s.Store.Q.ListAnnouncements(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	items := make([]Announcement, len(rows))
	for i, a := range rows {
		items[i] = announcementOf(a, now)
	}
	return AdminListAnnouncements200JSONResponse{Items: items}, nil
}

func announcementAudit(a sqlc.Announcement) map[string]any {
	return map[string]any{"title": a.Title, "level": a.Level, "enabled": a.Enabled, "starts_at": a.StartsAt, "ends_at": a.EndsAt,
		"body_length": len(a.Body)}
}

// AdminCreateAnnouncement implements POST /admin/announcements.
func (s *Server) AdminCreateAnnouncement(ctx context.Context, req AdminCreateAnnouncementRequestObject) (AdminCreateAnnouncementResponseObject, error) {
	f, err := announcementInput(req.Body)
	if err != nil {
		return nil, err
	}
	op := currentUser(ctx).ID
	id, _ := uuid.NewV7()
	row, err := s.Store.Q.CreateAnnouncement(ctx, sqlc.CreateAnnouncementParams{ID: id, Title: f.title, Body: f.body, Level: f.level,
		StartsAt: f.starts, EndsAt: f.ends, Enabled: f.enabled, CreatedBy: &op})
	if err != nil {
		return nil, err
	}
	s.audit(ctx, "announcement.create", "announcement", id.String(), nil, announcementAudit(row))
	return AdminCreateAnnouncement201JSONResponse(announcementOf(row, time.Now().UTC())), nil
}

// AdminUpdateAnnouncement implements PATCH /admin/announcements/{id}.
func (s *Server) AdminUpdateAnnouncement(ctx context.Context, req AdminUpdateAnnouncementRequestObject) (AdminUpdateAnnouncementResponseObject, error) {
	f, err := announcementInput(req.Body)
	if err != nil {
		return nil, err
	}
	cur, err := s.Store.Q.GetAnnouncement(ctx, req.AnnouncementId)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	row, err := s.Store.Q.UpdateAnnouncement(ctx, sqlc.UpdateAnnouncementParams{ID: cur.ID, Title: f.title, Body: f.body, Level: f.level,
		StartsAt: f.starts, EndsAt: f.ends, Enabled: f.enabled})
	if err != nil {
		return nil, mapDomainErr(err)
	}
	s.audit(ctx, "announcement.update", "announcement", cur.ID.String(), announcementAudit(cur), announcementAudit(row))
	return AdminUpdateAnnouncement200JSONResponse(announcementOf(row, time.Now().UTC())), nil
}

// AdminDeleteAnnouncement implements DELETE /admin/announcements/{id}.
func (s *Server) AdminDeleteAnnouncement(ctx context.Context, req AdminDeleteAnnouncementRequestObject) (AdminDeleteAnnouncementResponseObject, error) {
	cur, err := s.Store.Q.GetAnnouncement(ctx, req.AnnouncementId)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if _, err := s.Store.Q.DeleteAnnouncement(ctx, cur.ID); err != nil {
		return nil, err
	}
	s.audit(ctx, "announcement.delete", "announcement", cur.ID.String(), announcementAudit(cur), nil)
	return AdminDeleteAnnouncement204Response{}, nil
}
