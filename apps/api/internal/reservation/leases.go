package reservation

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const acquireScript = `
for i, key in ipairs(KEYS) do
  if redis.call('EXISTS', key) == 1 then return {0, key} end
end
for i, key in ipairs(KEYS) do redis.call('SET', key, ARGV[1], 'PX', ARGV[2]) end
return {1, ''}`
const releaseScript = `
local released = 0
for i, key in ipairs(KEYS) do
  if redis.call('GET', key) == ARGV[1] then released = released + redis.call('DEL', key) end
end
return released`

type Leases struct {
	client           *redis.Client
	acquire, release *redis.Script
}

func NewLeases(addr string) *Leases {
	return &Leases{client: redis.NewClient(&redis.Options{Addr: addr}), acquire: redis.NewScript(acquireScript), release: redis.NewScript(releaseScript)}
}
func (l *Leases) Ping(ctx context.Context) error { return l.client.Ping(ctx).Err() }
func (l *Leases) Close() error                   { return l.client.Close() }
func keys(eventID string, seatIDs []string) []string {
	out := make([]string, len(seatIDs))
	for i, id := range seatIDs {
		out[i] = fmt.Sprintf("seat:%s:%s", eventID, id)
	}
	return out
}
func (l *Leases) Acquire(ctx context.Context, eventID string, seatIDs []string, token string, ttl time.Duration) (bool, error) {
	result, err := l.acquire.Run(ctx, l.client, keys(eventID, seatIDs), token, ttl.Milliseconds()).Slice()
	if err != nil {
		return false, err
	}
	return result[0].(int64) == 1, nil
}
func (l *Leases) Release(ctx context.Context, eventID string, seatIDs []string, token string) error {
	return l.release.Run(ctx, l.client, keys(eventID, seatIDs), token).Err()
}
func (l *Leases) Publish(ctx context.Context, eventID string) {
	_ = l.client.Publish(ctx, "inventory:"+eventID, time.Now().UnixMilli()).Err()
}
func (l *Leases) Subscribe(ctx context.Context, eventID string) *redis.PubSub {
	return l.client.Subscribe(ctx, "inventory:"+eventID)
}
