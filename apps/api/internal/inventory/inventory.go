// Package inventory is the Redis admission layer. Each event's seats are one
// bitmap (bit = seat index). A Lua script claims a block of seats atomically,
// records which seats belong to the claim's token, and schedules the claim's
// expiry, so Redis frees abandoned seats on its own.
//
// Redis never decides that a seat is sold. PostgreSQL is the authority; this
// layer only rejects requests that are certain to lose before they reach the
// database.
package inventory

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrNotReady means the event's bitmap is missing (first use or Redis data
// loss) and must be rebuilt from PostgreSQL before claims are admitted.
var ErrNotReady = errors.New("inventory is not loaded")

// Settle is added to every Redis expiry. A hold's PostgreSQL expiry is
// computed at transaction start, which happens at most HoldTxTimeout after the
// Redis claim, so Redis always frees seats after PostgreSQL does, never before.
const Settle = 2500 * time.Millisecond

// HoldTxTimeout bounds the PostgreSQL work that follows a successful claim.
const HoldTxTimeout = 2 * time.Second

// sweepLua frees seats whose claims expired. Shared by every script so the
// bitmap is self-healing under load.
const sweepLua = `
local function sweep(bits, exp, holds, now, limit)
  local due = redis.call('ZRANGEBYSCORE', exp, '-inf', now, 'LIMIT', 0, limit)
  for _, tok in ipairs(due) do
    local held = redis.call('HGET', holds, tok)
    if held then
      for idx in string.gmatch(held, '%d+') do redis.call('SETBIT', bits, idx, 0) end
      redis.call('HDEL', holds, tok)
    end
    redis.call('ZREM', exp, tok)
  end
  return #due
end
local function now_ms()
  local t = redis.call('TIME')
  return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
end
`

// claim: KEYS bits, exp, holds, ready. ARGV token, ttl_ms, idx...
// Returns {1, swept} on success, {0, idx} when idx is taken, {-1, 0} when not ready.
var claimScript = redis.NewScript(sweepLua + `
if redis.call('EXISTS', KEYS[4]) == 0 then return {-1, 0} end
local now = now_ms()
sweep(KEYS[1], KEYS[2], KEYS[3], now, 64)
for i = 3, #ARGV do
  if redis.call('GETBIT', KEYS[1], ARGV[i]) == 1 then return {0, tonumber(ARGV[i])} end
end
for i = 3, #ARGV do redis.call('SETBIT', KEYS[1], ARGV[i], 1) end
redis.call('HSET', KEYS[3], ARGV[1], table.concat(ARGV, ',', 3))
redis.call('ZADD', KEYS[2], now + tonumber(ARGV[2]), ARGV[1])
return {1, 0}
`)

// release: KEYS bits, exp, holds. ARGV token. Frees only the token's seats.
var releaseScript = redis.NewScript(`
local held = redis.call('HGET', KEYS[3], ARGV[1])
if not held then return 0 end
local n = 0
for idx in string.gmatch(held, '%d+') do redis.call('SETBIT', KEYS[1], idx, 0); n = n + 1 end
redis.call('HDEL', KEYS[3], ARGV[1])
redis.call('ZREM', KEYS[2], ARGV[1])
return n
`)

// confirm: KEYS bits, exp, holds, ready. ARGV token, idx...
// Marks the seats permanently taken (sold) and forgets the claim's expiry.
var confirmScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[4]) == 0 then return 0 end
for i = 2, #ARGV do redis.call('SETBIT', KEYS[1], ARGV[i], 1) end
redis.call('HDEL', KEYS[3], ARGV[1])
redis.call('ZREM', KEYS[2], ARGV[1])
return 1
`)

// sweep: KEYS bits, exp, holds, ready.
var sweepScript = redis.NewScript(sweepLua + `
if redis.call('EXISTS', KEYS[4]) == 0 then return 0 end
return sweep(KEYS[1], KEYS[2], KEYS[3], now_ms(), 1000)
`)

// Keys names one event's inventory at one version. The hash tag keeps every
// key in the same Redis Cluster slot so the Lua scripts stay legal there.
type Keys struct{ Bits, Exp, Holds, Ready, Rebuild string }

func KeysFor(eventID string, version int64) Keys {
	tag := fmt.Sprintf("fs:{%s:%d}", eventID, version)
	return Keys{Bits: tag + ":bits", Exp: tag + ":exp", Holds: tag + ":holds", Ready: tag + ":ready", Rebuild: tag + ":rebuild"}
}

type Inventory struct{ Client *redis.Client }

func New(addr string, poolSize int) *Inventory {
	return &Inventory{Client: redis.NewClient(&redis.Options{
		Addr: addr, PoolSize: poolSize, MinIdleConns: 8,
		DialTimeout: 500 * time.Millisecond, ReadTimeout: time.Second, WriteTimeout: time.Second,
	})}
}

func (inv *Inventory) Ping(ctx context.Context) error { return inv.Client.Ping(ctx).Err() }
func (inv *Inventory) Close() error                   { return inv.Client.Close() }

func idxArgs(prefix []any, indexes []int) []any {
	out := make([]any, 0, len(prefix)+len(indexes))
	out = append(out, prefix...)
	for _, i := range indexes {
		out = append(out, i)
	}
	return out
}

// Bits returns the raw bitmap. Missing keys read as an empty (all free) map.
func (inv *Inventory) Bits(ctx context.Context, k Keys) ([]byte, error) {
	b, err := inv.Client.Get(ctx, k.Bits).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	return b, err
}

// Claim atomically takes every seat in indexes for token, or none of them.
func (inv *Inventory) Claim(ctx context.Context, k Keys, token string, ttl time.Duration, indexes []int) (bool, error) {
	res, err := claimScript.Run(ctx, inv.Client, []string{k.Bits, k.Exp, k.Holds, k.Ready}, idxArgs([]any{token, (ttl + Settle).Milliseconds()}, indexes)...).Int64Slice()
	if err != nil {
		return false, err
	}
	switch res[0] {
	case 1:
		return true, nil
	case -1:
		return false, ErrNotReady
	}
	return false, nil
}

func (inv *Inventory) Release(ctx context.Context, k Keys, token string) error {
	return releaseScript.Run(ctx, inv.Client, []string{k.Bits, k.Exp, k.Holds}, token).Err()
}

func (inv *Inventory) Confirm(ctx context.Context, k Keys, token string, indexes []int) error {
	return confirmScript.Run(ctx, inv.Client, []string{k.Bits, k.Exp, k.Holds, k.Ready}, idxArgs([]any{token}, indexes)...).Err()
}

func (inv *Inventory) Sweep(ctx context.Context, k Keys) (int64, error) {
	return sweepScript.Run(ctx, inv.Client, []string{k.Bits, k.Exp, k.Holds, k.Ready}).Int64()
}

// Ready reports whether the event's bitmap is loaded.
func (inv *Inventory) Ready(ctx context.Context, k Keys) (bool, error) {
	n, err := inv.Client.Exists(ctx, k.Ready).Result()
	return n == 1, err
}

// LiveClaim is an unexpired PostgreSQL hold to restore into Redis.
type LiveClaim struct {
	Token     string
	ExpiresAt time.Time
	Indexes   []int
}

// Snapshot is PostgreSQL's view of which seats are taken.
type Snapshot struct {
	Sold   []int
	Claims []LiveClaim
}

// Load replaces the event's inventory with the snapshot in one MULTI/EXEC and
// marks it ready.
func (inv *Inventory) Load(ctx context.Context, k Keys, s Snapshot) error {
	_, err := inv.Client.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.Del(ctx, k.Bits, k.Exp, k.Holds, k.Ready)
		for _, i := range s.Sold {
			p.SetBit(ctx, k.Bits, int64(i), 1)
		}
		for _, c := range s.Claims {
			parts := make([]string, len(c.Indexes))
			for j, i := range c.Indexes {
				p.SetBit(ctx, k.Bits, int64(i), 1)
				parts[j] = strconv.Itoa(i)
			}
			p.HSet(ctx, k.Holds, c.Token, strings.Join(parts, ","))
			p.ZAdd(ctx, k.Exp, redis.Z{Score: float64(c.ExpiresAt.Add(Settle).UnixMilli()), Member: c.Token})
		}
		p.Set(ctx, k.Ready, time.Now().UTC().Format(time.RFC3339Nano), 0)
		return nil
	})
	return err
}

// Rebuild restores a lost inventory from PostgreSQL. Only one caller does the
// work; the others get ErrNotReady and retry. The winner first waits out
// HoldTxTimeout: claims are refused while the bitmap is missing, so after that
// pause every database transaction that won a claim before the loss has
// committed or aborted, and the snapshot is exact.
func (inv *Inventory) Rebuild(ctx context.Context, k Keys, snapshot func(context.Context) (Snapshot, error)) (bool, error) {
	ok, err := inv.Client.SetNX(ctx, k.Rebuild, "1", 15*time.Second).Result()
	if err != nil || !ok {
		return false, err
	}
	defer inv.Client.Del(context.Background(), k.Rebuild)
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-time.After(HoldTxTimeout + 500*time.Millisecond):
	}
	s, err := snapshot(ctx)
	if err != nil {
		return false, err
	}
	return true, inv.Load(ctx, k, s)
}

// Drop deletes every key of one inventory version.
func (inv *Inventory) Drop(ctx context.Context, k Keys) error {
	return inv.Client.Del(ctx, k.Bits, k.Exp, k.Holds, k.Ready, k.Rebuild).Err()
}

// assert: KEYS bits, ready. ARGV idx... Re-marks seats PostgreSQL has sold.
// Sold is permanent, so setting these bits can never make a seat wrongly free.
var assertScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[2]) == 0 then return 0 end
for i = 1, #ARGV do redis.call('SETBIT', KEYS[1], ARGV[i], 1) end
return #ARGV
`)

// AssertSold re-marks sold seats in the bitmap if it is loaded.
func (inv *Inventory) AssertSold(ctx context.Context, k Keys, indexes []int) error {
	if len(indexes) == 0 {
		return nil
	}
	return assertScript.Run(ctx, inv.Client, []string{k.Bits, k.Ready}, idxArgs(nil, indexes)...).Err()
}
