package rstate

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Panel sessions live at SessionKey(token) with a sliding TTL. Every user
// also has a set of their session tokens (UserSessionsKey) so all of a
// user's sessions can be revoked at once; revocation is a Redis write, so it
// takes effect on every replica immediately.

// UserSessionsKey is the set of a user's session tokens.
func (s *State) UserSessionsKey(user uuid.UUID) string { return s.k("usess", user.String()) }

// sessionKeyPrefix is SessionKey without the token.
func (s *State) sessionKeyPrefix() string { return s.SessionKey("") }

var createSessionScript = redis.NewScript(`
-- KEYS[1] session key, KEYS[2] user set; ARGV: data, ttl ms, token, session key prefix
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
-- Drop tokens whose session already expired so the set does not grow.
for _, t in ipairs(redis.call('SMEMBERS', KEYS[2])) do
  if redis.call('EXISTS', ARGV[4] .. t) == 0 then redis.call('SREM', KEYS[2], t) end
end
redis.call('SADD', KEYS[2], ARGV[3])
redis.call('PEXPIRE', KEYS[2], ARGV[2])
return 1
`)

// CreateSession stores a session and adds it to the user's set.
func (s *State) CreateSession(ctx context.Context, user uuid.UUID, token string, data []byte, ttl time.Duration) error {
	return createSessionScript.Run(ctx, s.R, []string{s.SessionKey(token), s.UserSessionsKey(user)},
		data, ttl.Milliseconds(), token, s.sessionKeyPrefix()).Err()
}

// TouchSession slides a session's TTL. All sessions share one TTL, so the
// session just touched is the one that lives longest and the user's set gets
// the same expiry.
func (s *State) TouchSession(ctx context.Context, user uuid.UUID, token string, ttl time.Duration) error {
	pipe := s.R.Pipeline()
	pipe.PExpire(ctx, s.SessionKey(token), ttl)
	pipe.SAdd(ctx, s.UserSessionsKey(user), token)
	pipe.PExpire(ctx, s.UserSessionsKey(user), ttl)
	_, err := pipe.Exec(ctx)
	return err
}

// DeleteSession removes one session (logout, or a session found invalid).
func (s *State) DeleteSession(ctx context.Context, user uuid.UUID, token string) error {
	pipe := s.R.TxPipeline()
	pipe.Del(ctx, s.SessionKey(token))
	if user != uuid.Nil {
		pipe.SRem(ctx, s.UserSessionsKey(user), token)
	}
	_, err := pipe.Exec(ctx)
	return err
}

var revokeSessionsScript = redis.NewScript(`
-- KEYS[1] user set; ARGV: session key prefix, token to keep ('' = none)
local n = 0
for _, t in ipairs(redis.call('SMEMBERS', KEYS[1])) do
  if t ~= ARGV[2] then
    redis.call('DEL', ARGV[1] .. t)
    redis.call('SREM', KEYS[1], t)
    n = n + 1
  end
end
return n
`)

// RevokeSessions deletes every session of a user except keep (pass "" to
// delete all of them) and returns how many were removed.
func (s *State) RevokeSessions(ctx context.Context, user uuid.UUID, keep string) (int64, error) {
	return revokeSessionsScript.Run(ctx, s.R, []string{s.UserSessionsKey(user)}, s.sessionKeyPrefix(), keep).Int64()
}

// UserSessions lists the session tokens currently recorded for a user.
func (s *State) UserSessions(ctx context.Context, user uuid.UUID) ([]string, error) {
	return s.R.SMembers(ctx, s.UserSessionsKey(user)).Result()
}
