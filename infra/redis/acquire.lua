-- Atomically leases every requested seat, or none. ARGV: token, ttl_ms.
for _, key in ipairs(KEYS) do
  if redis.call('EXISTS', key) == 1 then return {0, key} end
end
for _, key in ipairs(KEYS) do redis.call('SET', key, ARGV[1], 'PX', ARGV[2]) end
return {1, ''}
