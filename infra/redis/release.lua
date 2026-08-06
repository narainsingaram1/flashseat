-- Deletes only leases still owned by the supplied token.
local released = 0
for _, key in ipairs(KEYS) do
  if redis.call('GET', key) == ARGV[1] then released = released + redis.call('DEL', key) end
end
return released
